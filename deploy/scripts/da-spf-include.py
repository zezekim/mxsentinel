#!/usr/bin/env python3
"""
da-spf-include.py — add an SPF `include:` to the apex SPF record of every domain on a
DirectAdmin server, safely and idempotently.

DirectAdmin serves DNS from BIND zone files in /var/named/<domain>.db. This edits the
existing `v=spf1` TXT record in each zone: inserts `include:<HOST>` immediately before the
`all` mechanism, bumps the SOA serial, and reloads BIND.

SAFE BY DEFAULT:
  * DRY-RUN unless you pass --apply. Dry-run changes nothing; it prints what it would do.
  * Every modified zone is backed up first to a timestamped dir.
  * Idempotent: a zone that already has the include is skipped.
  * Records it can't parse with confidence (multi-string TXT, redirect= SPF) are SKIPPED
    and reported, never rewritten.
  * By default it does NOT create SPF where none exists (that's a bigger decision). Pass
    --add-missing to add `v=spf1 include:<HOST> ~all` to domains with no SPF.

USAGE:
    # See what would change (safe, no writes):
    sudo python3 da-spf-include.py

    # Apply to all zones + reload BIND:
    sudo python3 da-spf-include.py --apply

    # Test on one domain first (recommended):
    sudo python3 da-spf-include.py --apply --only example.com

    # Also create SPF for domains that have none:
    sudo python3 da-spf-include.py --apply --add-missing

CAVEATS:
  * If you run DirectAdmin secondary/slave DNS, the SOA serial bump here drives propagation.
  * DO NOT trigger a DirectAdmin "DNS rewrite" (task.queue action=rewrite&value=dns) after
    this — that regenerates zones from DA's templates and could revert these edits. A future
    panel edit of a given domain's DNS may also regenerate that one zone; re-run this if so.
  * SPF has a hard 10-DNS-lookup limit. This warns when a record looks close, but verify.
"""

import argparse
import datetime
import glob
import os
import re
import shutil
import subprocess
import sys

# ---- configuration -------------------------------------------------------------------------
INCLUDE_HOST = "spf.squidix.net"                 # the host to add as include:<HOST>
ZONE_DIR = "/var/named"                          # DirectAdmin BIND zone dir (RHEL/AlmaLinux)
BACKUP_ROOT = os.path.join(os.path.expanduser("~"), "spf-zone-backups")  # writable, outside zone dir
RELOAD_CMD = ["rndc", "reload"]                  # how to reload BIND after applying
# --------------------------------------------------------------------------------------------

INCLUDE_TOKEN = f"include:{INCLUDE_HOST}"
ALL_RE = re.compile(r"^[-~?+]?all$", re.IGNORECASE)
# Mechanisms that each cost a DNS lookup (for the 10-lookup warning).
LOOKUP_MECHS = ("include:", "a:", "a ", "mx:", "mx ", "ptr", "exists:", "redirect=")


def find_spf_string(txt_value_region: str):
    """Return the SPF content and its quote span within a TXT value region, or None.

    txt_value_region is everything after 'TXT' on the record line. We only handle the common
    single-quoted-string form:  "v=spf1 ...". Multi-string TXT ("..." "...") is rejected
    (returns ("MULTI", ...)) so we never corrupt a split record.
    """
    quotes = [m.start() for m in re.finditer(r'"', txt_value_region)]
    if len(quotes) < 2:
        return None
    if len(quotes) > 2:
        # More than one quoted string on the line — could be a split SPF; refuse to edit.
        inside_first = txt_value_region[quotes[0] + 1:quotes[1]]
        if "v=spf1" in inside_first.lower():
            return ("MULTI", None, None)
        return None
    start, end = quotes[0], quotes[1]
    content = txt_value_region[start + 1:end]
    if not content.lower().startswith("v=spf1"):
        return None
    return (content, start, end)


def lookup_count(spf: str) -> int:
    low = " " + spf.lower() + " "
    return sum(low.count(m) for m in LOOKUP_MECHS)


def add_include(spf: str):
    """Insert INCLUDE_TOKEN before the `all` mechanism. Returns (new_spf, note) or
    (None, reason) if it should be skipped."""
    if INCLUDE_TOKEN.lower() in spf.lower():
        return (None, "already-present")
    if "redirect=" in spf.lower():
        return (None, "uses-redirect")  # SPF delegated elsewhere; adding include is wrong
    tokens = spf.split()
    out, inserted = [], False
    for tok in tokens:
        if not inserted and ALL_RE.match(tok):
            out.append(INCLUDE_TOKEN)
            inserted = True
        out.append(tok)
    if not inserted:
        out.append(INCLUDE_TOKEN)  # no `all` mechanism — append at end
    return (" ".join(out), "ok")


def bump_soa_serial(lines):
    """Increment the SOA serial in a list of zone lines (in place). Returns True on success.

    Handles the common multi-line paren form:
        ... IN SOA ns. host. (
            2026072701 ; serial
    and a single-line inline SOA. Only the serial integer is touched.
    """
    for i, line in enumerate(lines):
        if re.search(r"\bSOA\b", line, re.IGNORECASE):
            # Case 1: serial on a following line (paren form): first standalone integer.
            for j in range(i, min(i + 8, len(lines))):
                m = re.search(r"(?<![\d.])(\d{1,10})(?![\d.])(\s*;.*serial|\s*;|\s*$)",
                              lines[j], re.IGNORECASE)
                # Prefer a line explicitly commented as the serial, else the SOA line's own.
                if m and ("serial" in lines[j].lower() or "(" in lines[i]):
                    old = int(m.group(1))
                    new = str(old + 1)
                    lines[j] = lines[j][:m.start(1)] + new + lines[j][m.end(1):]
                    return True
            # Case 2: inline SOA — serial is the first integer after the two hostnames,
            # allowing an optional "(" (e.g. `SOA ns. host. ( 2026072701 14400 ... )`).
            m = re.search(r"(SOA\s+\S+\s+\S+\s+\(?\s*)(\d+)", lines[i], re.IGNORECASE)
            if m:
                old = int(m.group(2))
                lines[i] = lines[i][:m.start(2)] + str(old + 1) + lines[i][m.end(2):]
                return True
    return False


def process_zone(path, add_missing):
    """Return a dict describing the planned/necessary change for one zone file."""
    domain = os.path.basename(path)[:-3]  # strip .db
    with open(path, "r", encoding="utf-8", errors="replace") as fh:
        lines = fh.readlines()

    result = {"domain": domain, "path": path, "status": None, "detail": "",
              "new_lines": None, "warn": None}

    spf_line_idx = None
    for idx, line in enumerate(lines):
        stripped = line.strip()
        if stripped.startswith(";"):
            continue
        m = re.search(r"\bTXT\b(.*)$", line)
        if not m:
            continue
        found = find_spf_string(m.group(1))
        if found is None:
            continue
        if found[0] == "MULTI":
            result["status"] = "skip"
            result["detail"] = "multi-string SPF TXT — edit manually"
            return result
        spf_line_idx = idx
        spf_content = found[0]
        break

    if spf_line_idx is None:
        # No SPF record in this zone.
        if not add_missing:
            result["status"] = "no-spf"
            result["detail"] = "no v=spf1 record (use --add-missing to create one)"
            return result
        new_lines = list(lines)
        record = f'@\t14400\tIN\tTXT\t"v=spf1 {INCLUDE_TOKEN} ~all"\n'
        # Insert after the last record line (before trailing blank lines is fine too).
        new_lines.append(record)
        if not bump_soa_serial(new_lines):
            result["warn"] = "could not bump SOA serial"
        result["status"] = "create"
        result["detail"] = f'+ TXT "v=spf1 {INCLUDE_TOKEN} ~all"'
        result["new_lines"] = new_lines
        return result

    new_spf, note = add_include(spf_content)
    if new_spf is None:
        result["status"] = "skip"
        result["detail"] = note  # already-present | uses-redirect
        return result

    # Rebuild the SPF line, replacing only the quoted content.
    old_line = lines[spf_line_idx]
    new_line = old_line.replace(f'"{spf_content}"', f'"{new_spf}"', 1)
    if new_line == old_line:
        result["status"] = "skip"
        result["detail"] = "could not splice new value (unexpected format)"
        return result

    new_lines = list(lines)
    new_lines[spf_line_idx] = new_line
    if not bump_soa_serial(new_lines):
        result["warn"] = "could not bump SOA serial"

    lc = lookup_count(new_spf)
    if lc >= 10:
        result["warn"] = f"SPF now has ~{lc} DNS-lookup mechanisms (limit is 10)"

    result["status"] = "modify"
    result["detail"] = f'{spf_content}  ->  {new_spf}'
    result["new_lines"] = new_lines
    return result


def main():
    ap = argparse.ArgumentParser(description="Add an SPF include to all DirectAdmin domains.")
    ap.add_argument("--apply", action="store_true", help="write changes + reload (default: dry-run)")
    ap.add_argument("--add-missing", action="store_true", help="create SPF for domains that have none")
    ap.add_argument("--only", metavar="DOMAIN", help="operate on a single domain only")
    ap.add_argument("--zone-dir", default=ZONE_DIR)
    ap.add_argument("--backup-dir", default=BACKUP_ROOT, help=f"backup root (default: {BACKUP_ROOT})")
    ap.add_argument("--no-reload", action="store_true", help="don't reload BIND after applying")
    args = ap.parse_args()

    if args.only:
        paths = [os.path.join(args.zone_dir, f"{args.only}.db")]
        if not os.path.isfile(paths[0]):
            sys.exit(f"zone file not found: {paths[0]}")
    else:
        paths = sorted(glob.glob(os.path.join(args.zone_dir, "*.db")))
    if not paths:
        sys.exit(f"no zone files (*.db) found in {args.zone_dir}")

    mode = "APPLY" if args.apply else "DRY-RUN"
    print(f"== SPF include tool ({mode}) — adding {INCLUDE_TOKEN} — {len(paths)} zone(s) ==\n")

    to_write, counts = [], {"modify": 0, "create": 0, "skip": 0, "no-spf": 0}
    for path in paths:
        try:
            r = process_zone(path, args.add_missing)
        except Exception as e:  # never let one zone abort the run
            print(f"[ERROR ] {os.path.basename(path)}: {e}")
            continue
        counts[r["status"]] = counts.get(r["status"], 0) + 1
        tag = {"modify": "MODIFY", "create": "CREATE", "skip": "skip  ",
               "no-spf": "no-spf"}[r["status"]]
        print(f"[{tag}] {r['domain']}")
        if r["detail"]:
            print(f"         {r['detail']}")
        if r["warn"]:
            print(f"         !! {r['warn']}")
        if r["new_lines"] is not None:
            to_write.append(r)

    print(f"\nSummary: {counts['modify']} modify, {counts.get('create',0)} create, "
          f"{counts['skip']} skip, {counts['no-spf']} no-spf")

    if not args.apply:
        print("\nDRY-RUN — nothing written. Re-run with --apply to make these changes.")
        return
    if not to_write:
        print("\nNothing to change.")
        return

    stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
    backup_dir = os.path.join(args.backup_dir, f"spf-backup-{stamp}")
    os.makedirs(backup_dir, exist_ok=True)
    print(f"\nBacking up {len(to_write)} zone(s) to {backup_dir}")

    for r in to_write:
        shutil.copy2(r["path"], os.path.join(backup_dir, os.path.basename(r["path"])))
        with open(r["path"], "w", encoding="utf-8") as fh:
            fh.writelines(r["new_lines"])
        print(f"  wrote {r['domain']}")

    if args.no_reload:
        print("\n--no-reload set; skipping BIND reload. Reload manually to serve changes.")
        return
    print(f"\nReloading BIND: {' '.join(RELOAD_CMD)}")
    try:
        subprocess.run(RELOAD_CMD, check=True)
        print("Reload OK.")
    except Exception as e:
        print(f"!! reload failed ({e}). Zones written; reload manually (rndc reload / "
              "systemctl reload named).")


if __name__ == "__main__":
    main()
