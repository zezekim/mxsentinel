"use client";

import { useState } from "react";

const eximConfig = `# WHM > Exim Configuration Manager > Advanced Editor

# --- ROUTERS START ---
send_via_mxsentinel:
  driver = manualroute
  domains = ! +local_domains
  transport = mxsentinel_smtp
  route_list = * relay.example.com::587
  no_more

# --- TRANSPORTS START ---
mxsentinel_smtp:
  driver = smtp
  hosts_require_auth = relay.example.com
  hosts_require_tls = relay.example.com

# --- AUTHS START ---
mxsentinel_login:
  driver = plaintext
  public_name = LOGIN
  client_send = : relay@relay.example.com : YOUR_SMTP_PASSWORD`;

const swaksTest = `swaks --to you@gmail.com --from test@yourdomain.com \\
  --server relay.example.com --port 587 --tls \\
  --auth LOGIN --auth-user relay@relay.example.com --auth-password 'YOUR_SMTP_PASSWORD'`;

function Code({ children }: { children: string }) {
  return <pre className="doc-code">{children}</pre>;
}

function CpanelSetupTab() {
  return (
    <div className="doc-wrap">
      <h1>Connect a cPanel / WHM server</h1>
      <p className="section-desc">
        Relay all outbound mail from your cPanel/WHM server through MX Sentinel — a drop-in
        replacement for MailChannels. cPanel keeps DKIM-signing each customer&apos;s mail; MX
        Sentinel relays it across your IP pool, authenticates it, filters outbound abuse, and
        gives you per-message visibility.
      </p>

      <h2>Connection settings</h2>
      <div className="table-wrap">
        <table>
          <tbody>
            <tr><td>Host</td><td>your relay hostname (e.g. <code>relay.example.com</code>)</td></tr>
            <tr><td>Port</td><td><code>587</code> (submission)</td></tr>
            <tr><td>Encryption</td><td>STARTTLS (required)</td></tr>
            <tr><td>Auth</td><td>AUTH LOGIN / PLAIN</td></tr>
            <tr><td>Username / Password</td><td>the SMTP user you create in step 1</td></tr>
          </tbody>
        </table>
      </div>

      <h2>Step 1 — Create one SMTP user</h2>
      <p>
        Go to <strong>SMTP Users</strong> → add a user (a full address like{" "}
        <code>relay@relay.example.com</code> is conventional) and let it suggest a strong
        password. <strong>Your whole cPanel server authenticates with this single
        credential</strong> — you do not create one per customer.
      </p>

      <h2>Step 2 — Point WHM / Exim at the relay</h2>
      <p>
        In WHM → <em>Service Configuration → Exim Configuration Manager → Advanced Editor</em>,
        add a router, transport, and authenticator (replace the host, user, and password), then
        restart Exim. This replaces the MailChannels configuration.
      </p>
      <Code>{eximConfig}</Code>

      <h2>Step 3 — DKIM: nothing to do</h2>
      <p>
        cPanel already DKIM-signs each account&apos;s mail and publishes the key in the
        customer&apos;s DNS. MX Sentinel passes that signature through untouched, so mail stays
        authenticated for the customer&apos;s own domain (<code>dmarc=pass</code> via DKIM).
      </p>

      <h2>Step 4 — Per-domain DNS (SPF + DMARC)</h2>
      <p>
        For each sending domain, publish (DKIM is already handled by cPanel):
      </p>
      <ul className="doc-list">
        <li>
          <strong>SPF</strong> (TXT <code>@</code>): add the relay —{" "}
          <code>v=spf1 include:&lt;your SPF endpoint&gt; ~all</code> (set the endpoint under{" "}
          <strong>Settings</strong>).
        </li>
        <li>
          <strong>DMARC</strong> (TXT <code>_dmarc</code>):{" "}
          <code>v=DMARC1; p=none; rua=mailto:dmarc@yourdomain.com</code> — start at{" "}
          <code>p=none</code>, tighten to <code>quarantine</code>/<code>reject</code> later.
        </li>
      </ul>
      <p>
        To monitor a domain&apos;s DNS posture in the dashboard, register it (bulk-import the
        whole server&apos;s list straight from cPanel):
      </p>
      <Code>{"cat /etc/trueuserdomains | mxctl domain import --tenant <slug>"}</Code>

      <h2>Step 5 — Send a test &amp; verify</h2>
      <p>From the cPanel box (or anywhere that can reach the relay):</p>
      <Code>{swaksTest}</Code>
      <ul className="doc-list">
        <li>Watch it in <strong>Message Explorer</strong> (filter by domain or SMTP user).</li>
        <li>
          Open the received message&apos;s headers — confirm <code>spf=pass</code>,{" "}
          <code>dkim=pass</code>, <code>dmarc=pass</code>.
        </li>
      </ul>

      <h2>Step 6 — Warm up &amp; monitor</h2>
      <ul className="doc-list">
        <li>
          <strong>Warm up</strong> new sending IPs: ramp volume gradually over days/weeks — a
          cold IP that suddenly blasts gets throttled or spam-foldered.
        </li>
        <li>
          <strong>Domains</strong> flags broken SPF/DKIM/DMARC; <strong>Incidents</strong>{" "}
          surfaces abuse (a customer whose mail recipients reject as spam is auto-flagged), and
          outbound rate limits cap any single sender — so one compromised account can&apos;t
          burn the shared IP pool.
        </li>
      </ul>

      <p className="doc-footer">
        Outbound spam/malware filtering (rspamd + ClamAV), per-domain rate limits, and random
        IP rotation are configured on the relay host by the installer — see the project&apos;s{" "}
        <code>docs/deploy-relay.md</code> for the relay-side runbook.
      </p>
    </div>
  );
}

function IntegrationsApiTab() {
  return (
    <div className="doc-wrap">
      <h1>Integrations API</h1>
      <p className="section-desc">
        MX Sentinel can sync with cPanel/WHM servers to discover the account→domain mapping,
        aggregate email telemetry per cPanel account, and push metrics to WHMCS for billing
        visibility. All WHM and WHMCS API calls are made server-side — credentials never reach
        the browser.
      </p>

      <h2>Pipeline</h2>
      <ol className="doc-list">
        <li><strong>Sync</strong> — <code>cpaneld</code> polls WHM API → discovers accounts + domain types</li>
        <li><strong>Match</strong> — <code>smtp_events.from_domain</code> is resolved against <code>cpanel_domains</code> in PostgreSQL</li>
        <li><strong>Aggregate</strong> — ClickHouse groups by <code>from_domain</code>; Go joins and sums per account</li>
        <li><strong>Push</strong> — WHMCS <code>AddBillableItem</code> records a summary per client</li>
      </ol>

      <h2>Authentication</h2>
      <p>
        All <code>/v1/integrations/*</code> endpoints require a <code>Bearer</code> token.
        Mutation endpoints require <strong>admin</strong> scope; read endpoints require <strong>read</strong> scope.
      </p>

      <h2>Configuration</h2>
      <div className="table-wrap">
        <table>
          <thead><tr><th>Variable</th><th>Description</th></tr></thead>
          <tbody>
            <tr>
              <td><code>MXS_ENCRYPTION_KEY</code></td>
              <td>
                64-char hex (32 bytes). AES-256-GCM key for stored credentials.
                Generate: <code>openssl rand -hex 32</code>. If unset, credentials stored as plaintext.
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <h2>cPanel Server Endpoints</h2>

      <h3><code>GET /v1/integrations/cpanel</code></h3>
      <p>List all configured WHM servers. <code>api_token</code> is always redacted as <code>"***"</code>.</p>
      <Code>{`{
  "servers": [{
    "id": "019123ab-...",
    "label": "Production WHM",
    "hostname": "server1.example.com",
    "port": 2087,
    "username": "root",
    "api_token": "***",
    "verify_ssl": true,
    "sync_interval": 14400,
    "sync_status": "ok",
    "account_count": 142
  }]
}`}</Code>

      <h3><code>POST /v1/integrations/cpanel</code> <span style={{ fontSize: "0.8rem", color: "#6b7280" }}>admin</span></h3>
      <p>
        Add a WHM server. The API pings the WHM endpoint before saving — returns{" "}
        <code>422 ping_failed</code> if unreachable.
      </p>
      <Code>{`{
  "label": "Production WHM",
  "hostname": "server1.example.com",
  "port": 2087,
  "username": "root",
  "api_token": "WHM_API_TOKEN",
  "verify_ssl": true,
  "sync_interval": 14400
}`}</Code>

      <h3><code>PATCH /v1/integrations/cpanel/{"{id}"}</code> <span style={{ fontSize: "0.8rem", color: "#6b7280" }}>admin</span></h3>
      <p>Update label, verify_ssl, or sync_interval. Hostname/username/token are immutable — delete and recreate.</p>

      <h3><code>DELETE /v1/integrations/cpanel/{"{id}"}</code> <span style={{ fontSize: "0.8rem", color: "#6b7280" }}>admin</span></h3>
      <p>Remove server and all synced accounts/domains (CASCADE). Returns <code>204</code>.</p>

      <h3><code>POST /v1/integrations/cpanel/{"{id}"}/sync</code> <span style={{ fontSize: "0.8rem", color: "#6b7280" }}>admin</span></h3>
      <p>Trigger an immediate background sync. Returns <code>202 {"{ \"queued\": true }"}</code>. Poll <code>GET /{"{id}"}</code> for result.</p>

      <h3><code>GET /v1/integrations/cpanel/{"{id}"}/accounts</code></h3>
      <p>List synced cPanel accounts. Query params: <code>suspended=true|false</code>, <code>search</code>.</p>

      <h3><code>GET /v1/integrations/cpanel/{"{id}"}/accounts/{"{username}"}</code></h3>
      <p>Single account with all domains + 30-day email metrics (delivered, deferred, bounced, rejected, total).</p>

      <h3><code>GET /v1/integrations/cpanel/lookup?domain={"{domain}"}</code></h3>
      <p>Reverse lookup: which cPanel account owns a domain? Returns <code>404</code> if not synced.</p>

      <h2>WHMCS Connection Endpoints</h2>

      <h3><code>GET /v1/integrations/whmcs</code></h3>
      <p>List all WHMCS connections. <code>api_secret</code> is never returned.</p>

      <h3><code>POST /v1/integrations/whmcs</code> <span style={{ fontSize: "0.8rem", color: "#6b7280" }}>admin</span></h3>
      <p>Add a connection. Pings WHMCS before saving.</p>
      <Code>{`{
  "label": "Main Billing",
  "api_url": "https://billing.example.com/includes/api.php",
  "api_identifier": "YOUR_IDENTIFIER",
  "api_secret": "YOUR_SECRET",
  "push_frequency": "daily",
  "push_metric_fields": ["delivered","bounced","rejected","total"]
}`}</Code>

      <h3><code>PATCH /v1/integrations/whmcs/{"{id}"}</code> <span style={{ fontSize: "0.8rem", color: "#6b7280" }}>admin</span></h3>
      <p>Update label, push_frequency, push_metric_fields, or enabled flag.</p>

      <h3><code>DELETE /v1/integrations/whmcs/{"{id}"}</code> <span style={{ fontSize: "0.8rem", color: "#6b7280" }}>admin</span></h3>
      <p>Remove connection and push log. Returns <code>204</code>.</p>

      <h3><code>POST /v1/integrations/whmcs/{"{id}"}/push</code> <span style={{ fontSize: "0.8rem", color: "#6b7280" }}>admin</span></h3>
      <p>Trigger immediate metrics push in background. Returns <code>202</code>.</p>

      <h3><code>GET /v1/integrations/whmcs/{"{id}"}/log</code></h3>
      <p>Push history. Query: <code>limit</code> (default 20, max 100). Status values: <code>ok</code> | <code>partial</code> | <code>error</code>.</p>

      <h2>Metrics Export</h2>
      <h3><code>GET /v1/integrations/metrics?server_id={"{id}"}&amp;from={"{RFC3339}"}&amp;to={"{RFC3339}"}</code></h3>
      <p>Per-account email metrics for an arbitrary time range (default: last 30 days).</p>
      <Code>{`{
  "server_id": "019123ab-...",
  "period": { "from": "2026-05-11T00:00:00Z", "to": "2026-06-10T00:00:00Z" },
  "accounts": [{
    "username": "johndoe",
    "primary_domain": "johndoe.com",
    "owner_email": "john@example.com",
    "metrics": { "delivered": 4821, "deferred": 33, "bounced": 12, "rejected": 5, "total": 4871 }
  }]
}`}</Code>

      <h2>WHMCS Push Behavior</h2>
      <ol className="doc-list">
        <li>All cPanel servers for the tenant are loaded from PostgreSQL</li>
        <li>For each server the domain→account map is fetched</li>
        <li>ClickHouse is queried for <code>from_domain</code> metric counts in the billing period</li>
        <li>Results aggregated per cPanel account username in Go</li>
        <li>For each account with activity: look up WHMCS client by <code>owner_email</code>, fall back to domain lookup, skip if not found</li>
        <li><code>AddBillableItem</code> posted with <code>amount=0.00</code> and <code>invoiceaction=noinvoice</code></li>
        <li>Result recorded in <code>whmcs_push_log</code></li>
      </ol>
      <p>Zero-activity accounts are skipped — no billable item is created.</p>

      <h2>Running cpaneld</h2>
      <Code>{`make run-cpaneld
# or
MXS_ENCRYPTION_KEY=$(openssl rand -hex 32) go run ./cmd/cpaneld`}</Code>
      <p style={{ fontSize: "0.85rem", color: "#6b7280" }}>
        Sync interval per server (default 4h) and push frequency per connection (daily/weekly) are
        checked every 60 seconds by the daemon.
      </p>

      <h2>Error Codes</h2>
      <div className="table-wrap">
        <table>
          <thead><tr><th>Code</th><th>HTTP</th><th>Description</th></tr></thead>
          <tbody>
            <tr><td><code>not_found</code></td><td>404</td><td>Resource not found</td></tr>
            <tr><td><code>ping_failed</code></td><td>422</td><td>Cannot connect with provided credentials</td></tr>
            <tr><td><code>missing_field</code></td><td>400</td><td>Required field absent</td></tr>
            <tr><td><code>invalid_body</code></td><td>400</td><td>Request body is not valid JSON</td></tr>
            <tr><td><code>invalid_token</code></td><td>401</td><td>Bearer token missing or invalid</td></tr>
            <tr><td><code>forbidden</code></td><td>403</td><td>Token lacks required scope</td></tr>
            <tr><td><code>internal_error</code></td><td>500</td><td>Unexpected server error</td></tr>
          </tbody>
        </table>
      </div>

      <h2>Security Notes</h2>
      <ul className="doc-list">
        <li>Credentials encrypted at rest with AES-256-GCM when <code>MXS_ENCRYPTION_KEY</code> is set</li>
        <li><code>api_token</code> (WHM) and <code>api_secret</code> (WHMCS) are write-only — never returned in GET responses</li>
        <li>All WHM and WHMCS API calls are made server-side — credentials never reach the browser</li>
        <li><code>AddBillableItem</code> with <code>amount=0.00</code> — no charges are created automatically</li>
      </ul>
    </div>
  );
}

type Tab = "cpanel-setup" | "integrations-api";

export default function DocsPage() {
  const [activeTab, setActiveTab] = useState<Tab>("cpanel-setup");

  return (
    <>
      <div className="tabs">
        <button
          className={`tab-btn ${activeTab === "cpanel-setup" ? "tab-active" : ""}`}
          onClick={() => setActiveTab("cpanel-setup")}
        >
          cPanel Setup
        </button>
        <button
          className={`tab-btn ${activeTab === "integrations-api" ? "tab-active" : ""}`}
          onClick={() => setActiveTab("integrations-api")}
        >
          Integrations API
        </button>
      </div>

      {activeTab === "cpanel-setup" && <CpanelSetupTab />}
      {activeTab === "integrations-api" && <IntegrationsApiTab />}
    </>
  );
}
