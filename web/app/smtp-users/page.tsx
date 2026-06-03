"use client";

import { useEffect, useState, useCallback } from "react";
import {
  listSMTPUsers,
  createSMTPUser,
  setSMTPUserEnabled,
  resetSMTPUserPassword,
  deleteSMTPUser,
  type SMTPUser,
} from "@/lib/api";
import LoadingError from "@/components/LoadingError";

function fmt(ts: string): string {
  if (!ts) return "—";
  const d = new Date(ts);
  return isNaN(d.getTime()) ? ts : d.toLocaleString();
}

export default function SMTPUsersPage() {
  const [users, setUsers] = useState<SMTPUser[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Create form
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [domain, setDomain] = useState("");
  const [creating, setCreating] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const refresh = useCallback(() => {
    setLoading(true);
    setError(null);
    listSMTPUsers()
      .then((d) => setUsers(d.users))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    setFormError(null);
    setNotice(null);
    setCreating(true);
    try {
      const u = await createSMTPUser({
        username: username.trim(),
        password,
        domain: domain.trim() || undefined,
      });
      setNotice(
        `Created "${u.username}". Configure your smarthost to authenticate with this username and the password you just set.`,
      );
      setUsername("");
      setPassword("");
      setDomain("");
      refresh();
    } catch (err: unknown) {
      setFormError(err instanceof Error ? err.message : "Failed to create user");
    } finally {
      setCreating(false);
    }
  }

  async function handleToggle(u: SMTPUser) {
    try {
      await setSMTPUserEnabled(u.id, !u.enabled);
      refresh();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function handleResetPassword(u: SMTPUser) {
    const pw = window.prompt(`New password for "${u.username}" (min 8 characters):`);
    if (pw === null) return;
    if (pw.length < 8) {
      setError("Password must be at least 8 characters.");
      return;
    }
    try {
      await resetSMTPUserPassword(u.id, pw);
      setNotice(`Password updated for "${u.username}". Update the smarthost config with the new password.`);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function handleDelete(u: SMTPUser) {
    if (!window.confirm(`Delete SMTP user "${u.username}"? The relay will stop accepting its logins.`)) {
      return;
    }
    try {
      await deleteSMTPUser(u.id);
      refresh();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  return (
    <>
      <h1>SMTP Users</h1>
      <p className="section-desc">
        Credentials that smarthost clients (cPanel, Exim, applications) use to authenticate to the
        relay&apos;s submission port (587, STARTTLS, AUTH PLAIN/LOGIN). See the smarthost guide for client
        configuration examples.
      </p>

      <form className="filters" onSubmit={handleCreate}>
        <input
          type="text"
          placeholder="Username (e.g. mailer@send.example.com)"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="off"
          style={{ minWidth: 260 }}
          required
        />
        <input
          type="password"
          placeholder="Password (min 8 chars)"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="new-password"
          required
        />
        <input
          type="text"
          placeholder="Sending domain (optional)"
          value={domain}
          onChange={(e) => setDomain(e.target.value)}
        />
        <button type="submit" disabled={creating}>
          {creating ? "Adding…" : "Add user"}
        </button>
      </form>

      {formError && <p className="state-msg error">{formError}</p>}
      {notice && <p className="notice-banner">{notice}</p>}

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {users.length === 0 ? (
            <p className="no-results">No SMTP users yet. Add one above to let a smarthost send through the relay.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Username</th>
                    <th>Domain</th>
                    <th>Status</th>
                    <th>Created</th>
                    <th style={{ textAlign: "right" }}>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {users.map((u) => (
                    <tr key={u.id}>
                      <td>{u.username}</td>
                      <td>{u.domain || "—"}</td>
                      <td>
                        <span className={`badge ${u.enabled ? "badge-healthy" : "badge-unknown"}`}>
                          {u.enabled ? "enabled" : "disabled"}
                        </span>
                      </td>
                      <td style={{ whiteSpace: "nowrap" }}>{fmt(u.created_at)}</td>
                      <td>
                        <div className="row-actions">
                          <button type="button" className="btn-sm" onClick={() => handleToggle(u)}>
                            {u.enabled ? "Disable" : "Enable"}
                          </button>
                          <button type="button" className="btn-sm" onClick={() => handleResetPassword(u)}>
                            Reset password
                          </button>
                          <button type="button" className="btn-sm btn-sm-danger" onClick={() => handleDelete(u)}>
                            Delete
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </>
  );
}
