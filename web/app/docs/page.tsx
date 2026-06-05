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

export default function DocsPage() {
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
