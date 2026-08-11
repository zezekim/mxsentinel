// Command mxctl is the MX Sentinel operator CLI: run database migrations, set up the
// event bus, seed local data, and smoke-test the bus end to end.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"

	"github.com/zezekim/mxsentinel/internal/api"
	"github.com/zezekim/mxsentinel/internal/auth"
	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/obs"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/migrations"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// version is overridable at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var cfgPath string

	root := &cobra.Command{
		Use:           "mxctl",
		Short:         "MX Sentinel operator CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&cfgPath, "config", os.Getenv("MXS_CONFIG"),
		"path to YAML config file (env MXS_* overrides; defaults are local-dev)")

	loadCfg := func() (config.Config, error) { return config.Load(cfgPath) }

	root.AddCommand(
		versionCmd(),
		configCmd(loadCfg),
		migrateCmd(loadCfg),
		busCmd(loadCfg),
		seedCmd(loadCfg),
		apikeyCmd(loadCfg),
		messageCmd(loadCfg),
		userCmd(loadCfg),
		smtpUserCmd(loadCfg),
		domainCmd(loadCfg),
		ipPoolCmd(loadCfg),
		relayNodeCmd(loadCfg),
		tenantCmd(loadCfg),
	)
	return root
}

func tenantCmd(load func() (config.Config, error)) *cobra.Command {
	c := &cobra.Command{Use: "tenant", Short: "Manage tenants"}

	var name, slug, kind string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a tenant",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" || slug == "" {
				return fmt.Errorf("--name and --slug are required")
			}
			cfg, err := load()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pg, err := pgstore.New(ctx, cfg.Postgres)
			if err != nil {
				return err
			}
			defer pg.Close()
			id, err := pg.CreateTenant(ctx, pgstore.Tenant{Name: name, Slug: slug, Kind: kind})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created tenant %s (%s, slug %s)\n", id, name, slug)
			return nil
		},
	}
	create.Flags().StringVar(&name, "name", "", "tenant display name")
	create.Flags().StringVar(&slug, "slug", "", "tenant slug (unique)")
	create.Flags().StringVar(&kind, "kind", "enterprise", "hosting_provider|msp|enterprise")
	c.AddCommand(create)
	return c
}

func ipPoolCmd(load func() (config.Config, error)) *cobra.Command {
	c := &cobra.Command{Use: "ip-pool", Short: "Manage sending IP pools"}

	var tenantSlug, name, purpose, addresses string
	create := &cobra.Command{
		Use:   "create",
		Short: "Register a sending IP pool (so repd monitors its reputation)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tenantSlug == "" || name == "" || addresses == "" {
				return fmt.Errorf("--tenant, --name, and --addresses are required")
			}
			switch purpose {
			case "transactional", "marketing", "warmup", "mixed":
			default:
				return fmt.Errorf("invalid --purpose %q (transactional|marketing|warmup|mixed)", purpose)
			}
			var ips []string
			for _, a := range strings.Split(addresses, ",") {
				if a = strings.TrimSpace(a); a != "" {
					ips = append(ips, a)
				}
			}
			cfg, err := load()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pg, err := pgstore.New(ctx, cfg.Postgres)
			if err != nil {
				return err
			}
			defer pg.Close()
			tenant, err := pg.GetTenantBySlug(ctx, tenantSlug)
			if err != nil {
				return err
			}
			id, err := pg.CreateIPPool(ctx, tenant.ID, name, purpose, ips)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created ip pool %s (%s, purpose %s, %d address(es))\n", id, name, purpose, len(ips))
			return nil
		},
	}
	create.Flags().StringVar(&tenantSlug, "tenant", "", "tenant slug")
	create.Flags().StringVar(&name, "name", "", "pool name")
	create.Flags().StringVar(&purpose, "purpose", "mixed", "transactional|marketing|warmup|mixed")
	create.Flags().StringVar(&addresses, "addresses", "", "comma-separated IPs")
	c.AddCommand(create)
	return c
}

func relayNodeCmd(load func() (config.Config, error)) *cobra.Command {
	c := &cobra.Command{Use: "relay-node", Short: "Manage relay nodes"}

	var tenantSlug, hostname, ip, software string
	add := &cobra.Command{
		Use:   "add",
		Short: "Register a relay node (so its telemetry/reputation is attributed)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tenantSlug == "" || hostname == "" {
				return fmt.Errorf("--tenant and --hostname are required")
			}
			cfg, err := load()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pg, err := pgstore.New(ctx, cfg.Postgres)
			if err != nil {
				return err
			}
			defer pg.Close()
			tenant, err := pg.GetTenantBySlug(ctx, tenantSlug)
			if err != nil {
				return err
			}
			id, err := pg.CreateRelayNode(ctx, tenant.ID, hostname, ip, software)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "registered relay node %s (%s, ip %s)\n", id, hostname, ip)
			return nil
		},
	}
	add.Flags().StringVar(&tenantSlug, "tenant", "", "tenant slug")
	add.Flags().StringVar(&hostname, "hostname", "", "relay FQDN")
	add.Flags().StringVar(&ip, "ip", "", "primary outbound IP")
	add.Flags().StringVar(&software, "software", "postfix", "relay software")
	c.AddCommand(add)
	return c
}

func userCmd(load func() (config.Config, error)) *cobra.Command {
	c := &cobra.Command{Use: "user", Short: "Manage users"}

	var tenantSlug, email, password, role string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a user that can log in to the dashboard/API",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tenantSlug == "" || email == "" || password == "" {
				return fmt.Errorf("--tenant, --email, and --password are required")
			}
			cfg, err := load()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pg, err := pgstore.New(ctx, cfg.Postgres)
			if err != nil {
				return err
			}
			defer pg.Close()

			tenant, err := pg.GetTenantBySlug(ctx, tenantSlug)
			if err != nil {
				return err
			}
			hash, err := auth.HashPassword(password)
			if err != nil {
				return err
			}
			id, err := pg.CreateUser(ctx, tenant.ID, email, hash, role)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created user %s (%s, role %s) in tenant %s\n", id, email, role, tenantSlug)
			return nil
		},
	}
	create.Flags().StringVar(&tenantSlug, "tenant", "", "tenant slug (e.g. demo)")
	create.Flags().StringVar(&email, "email", "", "user email")
	create.Flags().StringVar(&password, "password", "", "user password")
	create.Flags().StringVar(&role, "role", "owner", "role: owner|admin|operator|viewer")

	var pwTenant, pwEmail, pwPassword string
	setPassword := &cobra.Command{
		Use:   "set-password",
		Short: "Reset a user's password (e.g. a locked-out owner)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if pwTenant == "" || pwEmail == "" || pwPassword == "" {
				return fmt.Errorf("--tenant, --email, and --password are required")
			}
			if len(pwPassword) < 8 {
				return fmt.Errorf("--password must be at least 8 characters")
			}
			cfg, err := load()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pg, err := pgstore.New(ctx, cfg.Postgres)
			if err != nil {
				return err
			}
			defer pg.Close()

			tenant, err := pg.GetTenantBySlug(ctx, pwTenant)
			if err != nil {
				return err
			}
			hash, err := auth.HashPassword(pwPassword)
			if err != nil {
				return err
			}
			found, err := pg.UpdateUserPasswordByEmail(ctx, tenant.ID, pwEmail, hash)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("no user %q in tenant %s", pwEmail, pwTenant)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "password updated for %s in tenant %s\n", pwEmail, pwTenant)
			return nil
		},
	}
	setPassword.Flags().StringVar(&pwTenant, "tenant", "", "tenant slug (e.g. demo)")
	setPassword.Flags().StringVar(&pwEmail, "email", "", "user email")
	setPassword.Flags().StringVar(&pwPassword, "password", "", "new password (min 8 chars)")

	c.AddCommand(create, setPassword)
	return c
}

func smtpUserCmd(load func() (config.Config, error)) *cobra.Command {
	c := &cobra.Command{Use: "smtp-user", Short: "Manage SMTP submission users (relay SASL auth)"}

	withStore := func(cmd *cobra.Command) (*pgstore.Store, context.Context, error) {
		cfg, err := load()
		if err != nil {
			return nil, nil, err
		}
		ctx := cmd.Context()
		pg, err := pgstore.New(ctx, cfg.Postgres)
		if err != nil {
			return nil, nil, err
		}
		return pg, ctx, nil
	}

	var tenantSlug, username, password, domain string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create an SMTP submission credential (a smarthost client authenticates with it)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tenantSlug == "" || username == "" || password == "" {
				return fmt.Errorf("--tenant, --username, and --password are required")
			}
			if len(password) < 8 {
				return fmt.Errorf("--password must be at least 8 characters")
			}
			pg, ctx, err := withStore(cmd)
			if err != nil {
				return err
			}
			defer pg.Close()
			tenant, err := pg.GetTenantBySlug(ctx, tenantSlug)
			if err != nil {
				return err
			}
			hash, err := auth.HashPassword(password)
			if err != nil {
				return err
			}
			id, err := pg.CreateSMTPUser(ctx, tenant.ID, username, hash, domain)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created smtp user %s (%s) in tenant %s\n", id, username, tenantSlug)
			return nil
		},
	}
	create.Flags().StringVar(&tenantSlug, "tenant", "", "tenant slug (e.g. demo)")
	create.Flags().StringVar(&username, "username", "", "SASL login (e.g. mailer@send.example.com)")
	create.Flags().StringVar(&password, "password", "", "password (min 8 chars)")
	create.Flags().StringVar(&domain, "domain", "", "associated sending domain (optional)")

	var listTenant string
	list := &cobra.Command{
		Use:   "list",
		Short: "List a tenant's SMTP submission users",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listTenant == "" {
				return fmt.Errorf("--tenant is required")
			}
			pg, ctx, err := withStore(cmd)
			if err != nil {
				return err
			}
			defer pg.Close()
			tenant, err := pg.GetTenantBySlug(ctx, listTenant)
			if err != nil {
				return err
			}
			users, err := pg.ListSMTPUsers(ctx, tenant.ID)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(users) == 0 {
				fmt.Fprintln(out, "no smtp users")
				return nil
			}
			for _, u := range users {
				state := "enabled"
				if !u.Enabled {
					state = "disabled"
				}
				fmt.Fprintf(out, "%s\t%s\t%s\n", u.Username, state, u.Domain)
			}
			return nil
		},
	}
	list.Flags().StringVar(&listTenant, "tenant", "", "tenant slug")

	var delTenant, delUsername string
	del := &cobra.Command{
		Use:   "delete",
		Short: "Delete an SMTP submission user by username",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if delTenant == "" || delUsername == "" {
				return fmt.Errorf("--tenant and --username are required")
			}
			pg, ctx, err := withStore(cmd)
			if err != nil {
				return err
			}
			defer pg.Close()
			tenant, err := pg.GetTenantBySlug(ctx, delTenant)
			if err != nil {
				return err
			}
			found, err := pg.DeleteSMTPUserByUsername(ctx, tenant.ID, delUsername)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("no smtp user %q in tenant %s", delUsername, delTenant)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted smtp user %s\n", delUsername)
			return nil
		},
	}
	del.Flags().StringVar(&delTenant, "tenant", "", "tenant slug")
	del.Flags().StringVar(&delUsername, "username", "", "SASL login to delete")

	c.AddCommand(create, list, del)
	return c
}

func domainCmd(load func() (config.Config, error)) *cobra.Command {
	c := &cobra.Command{Use: "domain", Short: "Manage sending domains (telemetry attribution + DNS monitoring)"}

	var tenantSlug, name string
	add := &cobra.Command{
		Use:   "add",
		Short: "Register a sending domain and print the DNS records to publish",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tenantSlug == "" || name == "" {
				return fmt.Errorf("--tenant and --name are required")
			}
			cfg, err := load()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pg, err := pgstore.New(ctx, cfg.Postgres)
			if err != nil {
				return err
			}
			defer pg.Close()
			tenant, err := pg.GetTenantBySlug(ctx, tenantSlug)
			if err != nil {
				return err
			}
			id, err := pg.CreateDomain(ctx, tenant.ID, name)
			if err != nil {
				return err
			}
			ms, _ := pg.GetMailSettings(ctx, tenant.ID)

			spf := "v=spf1 ip4:<your-relay-ip> ~all"
			if ms.SPFInclude != "" {
				spf = fmt.Sprintf("v=spf1 include:%s ~all", ms.SPFInclude)
			}
			policy := ms.DMARCPolicy
			if policy == "" {
				policy = "none"
			}
			dmarc := "v=DMARC1; p=" + policy
			if ms.DMARCRua != "" {
				dmarc += "; rua=mailto:" + ms.DMARCRua
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "registered domain %s (%s) in tenant %s\n", id, name, tenantSlug)
			fmt.Fprintln(out, "\nPublish for this domain (cPanel/Exim already signs + publishes DKIM — nothing to do for DKIM here):")
			fmt.Fprintf(out, "  SPF    TXT  %-26s %s\n", name, spf)
			fmt.Fprintf(out, "  DMARC  TXT  _dmarc.%-19s %s\n", name, dmarc)
			return nil
		},
	}
	add.Flags().StringVar(&tenantSlug, "tenant", "", "tenant slug")
	add.Flags().StringVar(&name, "name", "", "sending domain (e.g. clientdomain.com)")

	var listTenant string
	list := &cobra.Command{
		Use:   "list",
		Short: "List a tenant's registered domains",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listTenant == "" {
				return fmt.Errorf("--tenant is required")
			}
			cfg, err := load()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pg, err := pgstore.New(ctx, cfg.Postgres)
			if err != nil {
				return err
			}
			defer pg.Close()
			tenant, err := pg.GetTenantBySlug(ctx, listTenant)
			if err != nil {
				return err
			}
			domains, err := pg.ListDomains(ctx, tenant.ID)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(domains) == 0 {
				fmt.Fprintln(out, "no domains")
				return nil
			}
			for _, d := range domains {
				fmt.Fprintf(out, "%s\t%s\n", d.Name, d.Status)
			}
			return nil
		},
	}
	list.Flags().StringVar(&listTenant, "tenant", "", "tenant slug")

	var impTenant, impFile string
	imp := &cobra.Command{
		Use:   "import",
		Short: "Bulk-register sending domains from a list (stdin or --file)",
		Long: "Reads one domain per line from stdin (or --file). Tolerant of cPanel's\n" +
			"/etc/trueuserdomains format ('domain: user') and '#' comments — only the\n" +
			"domain is taken. Idempotent: existing domains are skipped.\n\n" +
			"  cat /etc/trueuserdomains | mxctl domain import --tenant demo",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if impTenant == "" {
				return fmt.Errorf("--tenant is required")
			}
			var r io.Reader = cmd.InOrStdin()
			if impFile != "" {
				f, err := os.Open(impFile)
				if err != nil {
					return err
				}
				defer f.Close()
				r = f
			}
			cfg, err := load()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pg, err := pgstore.New(ctx, cfg.Postgres)
			if err != nil {
				return err
			}
			defer pg.Close()
			tenant, err := pg.GetTenantBySlug(ctx, impTenant)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			var created, skipped, failed int
			sc := bufio.NewScanner(r)
			for sc.Scan() {
				name := normalizeDomain(sc.Text())
				if name == "" {
					continue
				}
				ok, err := pg.EnsureDomain(ctx, tenant.ID, name)
				switch {
				case err != nil:
					failed++
					fmt.Fprintf(cmd.ErrOrStderr(), "  %s: %v\n", name, err)
				case ok:
					created++
				default:
					skipped++
				}
			}
			if err := sc.Err(); err != nil {
				return err
			}
			fmt.Fprintf(out, "import complete: %d created, %d already present, %d failed (tenant %s)\n",
				created, skipped, failed, impTenant)
			return nil
		},
	}
	imp.Flags().StringVar(&impTenant, "tenant", "", "tenant slug")
	imp.Flags().StringVar(&impFile, "file", "", "read domains from this file instead of stdin")

	c.AddCommand(add, list, imp)
	return c
}

// normalizeDomain extracts a bare domain from a line that may be "domain: user", have a
// leading "*." wildcard, comments, or surrounding whitespace.
func normalizeDomain(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	// Take the first whitespace- or colon-delimited field (handles "domain: user").
	fields := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' || r == ':' })
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(fields[0], "*."))
}

func apikeyCmd(load func() (config.Config, error)) *cobra.Command {
	c := &cobra.Command{Use: "apikey", Short: "Manage API credentials"}

	withStore := func(cmd *cobra.Command) (*pgstore.Store, context.Context, error) {
		cfg, err := load()
		if err != nil {
			return nil, nil, err
		}
		ctx := cmd.Context()
		pg, err := pgstore.New(ctx, cfg.Postgres)
		if err != nil {
			return nil, nil, err
		}
		return pg, ctx, nil
	}

	var tenantSlug, name, scopes, expiresIn string
	var createJSON bool
	create := &cobra.Command{
		Use:   "create",
		Short: "Create an API token for a tenant (printed once)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tenantSlug == "" {
				return fmt.Errorf("--tenant is required")
			}
			scopeList, err := parseScopes(scopes)
			if err != nil {
				return err
			}
			expiresAt, err := parseExpiresIn(expiresIn)
			if err != nil {
				return err
			}
			pg, ctx, err := withStore(cmd)
			if err != nil {
				return err
			}
			defer pg.Close()

			tenant, err := pg.GetTenantBySlug(ctx, tenantSlug)
			if err != nil {
				return err
			}
			token, prefix, hash, err := api.GenerateToken()
			if err != nil {
				return err
			}
			id, err := pg.CreateAPICredential(ctx, tenant.ID, name, prefix, hash, scopeList, expiresAt)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			// --json emits one object and nothing else, so scripts can parse stdout wholesale.
			if createJSON {
				payload := struct {
					ID        string   `json:"id"`
					Tenant    string   `json:"tenant"`
					Name      string   `json:"name"`
					Token     string   `json:"token"`
					Prefix    string   `json:"prefix"`
					Scopes    []string `json:"scopes"`
					ExpiresAt string   `json:"expires_at,omitempty"`
				}{ID: id, Tenant: tenantSlug, Name: name, Token: token, Prefix: prefix, Scopes: scopeList}
				if expiresAt != nil {
					payload.ExpiresAt = expiresAt.Format(time.RFC3339)
				}
				return json.NewEncoder(out).Encode(payload)
			}
			fmt.Fprintf(out, "created api credential %s for tenant %s (scopes: %v)\n", id, tenantSlug, scopeList)
			fmt.Fprintf(out, "token (shown once, store it now):\n  %s\n", token)
			if expiresAt != nil {
				fmt.Fprintf(out, "expires: %s\n", expiresAt.Format(time.RFC3339))
			}
			return nil
		},
	}
	create.Flags().StringVar(&tenantSlug, "tenant", "", "tenant slug (e.g. demo)")
	create.Flags().StringVar(&name, "name", "dashboard", "credential name")
	create.Flags().StringVar(&scopes, "scopes", "read", "comma-separated scopes: read,write,admin as today; "+
		"relay = provision SMTP submission users (what the cPanel plugin needs); "+
		"provision = may mint read+relay keys via POST /v1/apikeys and nothing else")
	create.Flags().StringVar(&expiresIn, "expires-in", "", "expiry as a Go duration (e.g. 8760h); empty = never expires")
	create.Flags().BoolVar(&createJSON, "json", false, "print a single JSON object instead of prose")

	var listTenant string
	var listJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List a tenant's API credentials (never prints tokens)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listTenant == "" {
				return fmt.Errorf("--tenant is required")
			}
			pg, ctx, err := withStore(cmd)
			if err != nil {
				return err
			}
			defer pg.Close()
			tenant, err := pg.GetTenantBySlug(ctx, listTenant)
			if err != nil {
				return err
			}
			creds, err := pg.ListAPICredentials(ctx, tenant.ID)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if listJSON {
				type row struct {
					ID         string   `json:"id"`
					Name       string   `json:"name"`
					Prefix     string   `json:"prefix"`
					Scopes     []string `json:"scopes"`
					CreatedAt  string   `json:"created_at"`
					LastUsedAt string   `json:"last_used_at,omitempty"`
					ExpiresAt  string   `json:"expires_at,omitempty"`
					RevokedAt  string   `json:"revoked_at,omitempty"`
					Status     string   `json:"status"`
				}
				rows := make([]row, 0, len(creds)) // non-nil so an empty tenant marshals as []
				for _, cr := range creds {
					rows = append(rows, row{
						ID: cr.ID, Name: cr.Name, Prefix: cr.TokenPrefix, Scopes: cr.Scopes,
						CreatedAt: cr.CreatedAt, LastUsedAt: cr.LastUsedAt, ExpiresAt: cr.ExpiresAt,
						RevokedAt: cr.RevokedAt, Status: credentialStatus(cr),
					})
				}
				return json.NewEncoder(out).Encode(rows)
			}
			if len(creds) == 0 {
				fmt.Fprintln(out, "no api credentials")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tPREFIX\tSCOPES\tCREATED\tLAST USED\tEXPIRES\tSTATUS")
			for _, cr := range creds {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					cr.Name, cr.TokenPrefix, strings.Join(cr.Scopes, ","),
					shortTime(cr.CreatedAt), shortTime(cr.LastUsedAt), shortTime(cr.ExpiresAt),
					credentialStatus(cr))
			}
			return tw.Flush()
		},
	}
	list.Flags().StringVar(&listTenant, "tenant", "", "tenant slug")
	list.Flags().BoolVar(&listJSON, "json", false, "print the raw credential array as JSON")

	var revokeTenant, revokeName string
	revoke := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke a tenant's API credential by name (its token stops working immediately)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if revokeTenant == "" || revokeName == "" {
				return fmt.Errorf("--tenant and --name are required")
			}
			pg, ctx, err := withStore(cmd)
			if err != nil {
				return err
			}
			defer pg.Close()
			tenant, err := pg.GetTenantBySlug(ctx, revokeTenant)
			if err != nil {
				return err
			}
			found, err := pg.RevokeAPICredentialByName(ctx, tenant.ID, revokeName)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("no api credential named %q in tenant %s — nothing was revoked "+
					"(run: mxctl apikey list --tenant %s)", revokeName, revokeTenant, revokeTenant)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"revoked api credential %q in tenant %s — its token no longer authenticates\n",
				revokeName, revokeTenant)
			return nil
		},
	}
	revoke.Flags().StringVar(&revokeTenant, "tenant", "", "tenant slug")
	revoke.Flags().StringVar(&revokeName, "name", "", "credential name to revoke")

	c.AddCommand(create, list, revoke)
	return c
}

// parseExpiresIn turns a Go duration string into an absolute expiry. Empty means "never
// expires", which the store represents as a nil *time.Time.
func parseExpiresIn(s string) (*time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return nil, fmt.Errorf("invalid --expires-in %q (want a Go duration like 8760h): %w", s, err)
	}
	if d <= 0 {
		return nil, fmt.Errorf("invalid --expires-in %q: must be positive", s)
	}
	t := time.Now().Add(d).UTC()
	return &t, nil
}

// credentialStatus reports whether a credential would authenticate right now. "expired" is
// distinct from "active" because auth filters on expires_at as well as revoked_at — an
// enrolled server whose year is up presents as a working credential in every other column,
// and calling that "active" would send an operator hunting the wrong fault.
func credentialStatus(cr pgstore.APICredentialInfo) string {
	if cr.RevokedAt != "" {
		return "revoked"
	}
	if cr.ExpiresAt != "" {
		if exp, err := time.Parse(time.RFC3339, cr.ExpiresAt); err == nil && !exp.After(time.Now()) {
			return "expired"
		}
	}
	return "active"
}

// shortTime renders an RFC3339 timestamp for table display; "" (never used / no expiry)
// becomes a dash, and anything unparseable is passed through untouched.
func shortTime(s string) string {
	if s == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func messageCmd(load func() (config.Config, error)) *cobra.Command {
	c := &cobra.Command{Use: "message", Short: "Message tools (shareable delivery-trace links)"}

	var tenantSlug, queueID, label string
	var ttlHours int
	share := &cobra.Command{
		Use:   "share",
		Short: "Mint a public delivery-trace link for one message (by relay queue id)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tenantSlug == "" || queueID == "" {
				return fmt.Errorf("--tenant and --queue-id are required")
			}
			cfg, err := load()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pg, err := pgstore.New(ctx, cfg.Postgres)
			if err != nil {
				return err
			}
			defer pg.Close()

			tenant, err := pg.GetTenantBySlug(ctx, tenantSlug)
			if err != nil {
				return err
			}

			// Verify the message exists for this tenant and grab its Message-ID from telemetry.
			ch, err := chstore.New(ctx, cfg.ClickHouse)
			if err != nil {
				return fmt.Errorf("connect clickhouse: %w", err)
			}
			trace, err := ch.QueryMessageTrace(ctx, tenant.ID, queueID)
			if err != nil {
				return fmt.Errorf("look up message: %w", err)
			}
			if len(trace.Events) == 0 {
				return fmt.Errorf("no message found for queue id %q in tenant %q", queueID, tenantSlug)
			}

			token, prefix, hash, err := api.GenerateShareToken()
			if err != nil {
				return err
			}
			var expiresAt *time.Time
			if ttlHours > 0 {
				t := time.Now().Add(time.Duration(ttlHours) * time.Hour)
				expiresAt = &t
			}
			id, err := pg.CreateShareLink(ctx, tenant.ID, queueID, trace.MessageID, prefix, hash, label, "", expiresAt)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "created share link %s for message %s (tenant %s)\n", id, queueID, tenantSlug)
			base := strings.TrimRight(os.Getenv("MXS_PUBLIC_BASE_URL"), "/")
			if base != "" {
				fmt.Fprintf(out, "url (shown once):\n  %s/trace/%s\n", base, token)
			} else {
				fmt.Fprintf(out, "path (shown once — prefix with your dashboard origin):\n  /trace/%s\n", token)
			}
			if expiresAt != nil {
				fmt.Fprintf(out, "expires: %s\n", expiresAt.UTC().Format(time.RFC3339))
			}
			return nil
		},
	}
	share.Flags().StringVar(&tenantSlug, "tenant", "", "tenant slug (e.g. sentinel)")
	share.Flags().StringVar(&queueID, "queue-id", "", "relay queue id of the message (e.g. 759CB8A8FD)")
	share.Flags().StringVar(&label, "label", "", "optional label shown on the trace page")
	share.Flags().IntVar(&ttlHours, "ttl-hours", 0, "optional link expiry in hours (0 = never)")

	c.AddCommand(share)
	return c
}

// parseScopes splits/validates a comma-separated scope list (read|write|relay|provision|admin).
func parseScopes(s string) ([]string, error) {
	valid := map[string]bool{
		api.ScopeRead:      true,
		api.ScopeWrite:     true,
		api.ScopeRelay:     true,
		api.ScopeProvision: true,
		api.ScopeAdmin:     true,
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range strings.Split(s, ",") {
		sc := strings.TrimSpace(strings.ToLower(raw))
		if sc == "" {
			continue
		}
		if !valid[sc] {
			return nil, fmt.Errorf("invalid scope %q (allowed: read, write, relay, provision, admin)", sc)
		}
		if !seen[sc] {
			seen[sc] = true
			out = append(out, sc)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one scope is required")
	}
	return out, nil
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print mxctl version",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "mxctl", version)
		},
	}
}

func configCmd(load func() (config.Config, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print the resolved configuration (secrets redacted)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := load()
			if err != nil {
				return err
			}
			cfg.Postgres.DSN = redact(cfg.Postgres.DSN)
			cfg.ClickHouse.Password = redact(cfg.ClickHouse.Password)
			cfg.Redis.Password = redact(cfg.Redis.Password)
			cfg.ObjectStore.SecretKey = redact(cfg.ObjectStore.SecretKey)
			fmt.Fprintf(cmd.OutOrStdout(), "%+v\n", cfg)
			return nil
		},
	}
}

func migrateCmd(load func() (config.Config, error)) *cobra.Command {
	var target string

	c := &cobra.Command{
		Use:   "migrate [up|down|status|version]",
		Short: "Run database migrations",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := load()
			if err != nil {
				return err
			}
			mc := migrations.Command(args[0])
			runPG := target == "all" || target == "postgres"
			runCH := target == "all" || target == "clickhouse"
			if !runPG && !runCH {
				return fmt.Errorf("invalid --target %q (want postgres|clickhouse|all)", target)
			}
			if runPG {
				fmt.Fprintln(cmd.OutOrStdout(), "==> postgres")
				if err := migrations.Postgres(cfg.Postgres, mc); err != nil {
					return fmt.Errorf("postgres migrate: %w", err)
				}
			}
			if runCH {
				fmt.Fprintln(cmd.OutOrStdout(), "==> clickhouse")
				if err := migrations.ClickHouse(cfg.ClickHouse, mc); err != nil {
					return fmt.Errorf("clickhouse migrate: %w", err)
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&target, "target", "all", "which database: postgres|clickhouse|all")
	return c
}

func busCmd(load func() (config.Config, error)) *cobra.Command {
	c := &cobra.Command{Use: "bus", Short: "Event bus operations"}

	ensure := &cobra.Command{
		Use:   "ensure",
		Short: "Create or update all JetStream streams",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := load()
			if err != nil {
				return err
			}
			bus, err := connectBus(cfg)
			if err != nil {
				return err
			}
			defer bus.Close()
			if err := bus.EnsureStreams(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "streams ensured")
			return nil
		},
	}

	selftest := &cobra.Command{
		Use:   "selftest",
		Short: "Publish one of each event family and read it back (requires NATS running)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := load()
			if err != nil {
				return err
			}
			return busSelftest(cmd.Context(), cmd.OutOrStdout(), cfg)
		},
	}

	c.AddCommand(ensure, selftest)
	return c
}

func seedCmd(load func() (config.Config, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "seed",
		Short: "Insert a demo tenant + domain into PostgreSQL (local dev)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := load()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pg, err := pgstore.New(ctx, cfg.Postgres)
			if err != nil {
				return err
			}
			defer pg.Close()

			tid, err := pg.CreateTenant(ctx, pgstore.Tenant{Name: "Demo Co", Slug: "demo", Kind: "enterprise"})
			if err != nil {
				return err
			}
			did, err := pg.CreateDomain(ctx, tid, "example.com")
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "seeded tenant=%s domain=%s (example.com)\n", tid, did)
			return nil
		},
	}
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

func connectBus(cfg config.Config) (*events.Bus, error) {
	v, err := events.NewValidator()
	if err != nil {
		return nil, err
	}
	log := obs.NewLogger("mxctl", cfg.LogLevel)
	return events.Connect(cfg.NATS.URL, "mxctl", v, log)
}

func busSelftest(ctx context.Context, out io.Writer, cfg config.Config) error {
	bus, err := connectBus(cfg)
	if err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureStreams(ctx); err != nil {
		return err
	}

	const tenantID = "00000000-0000-0000-0000-000000000001"
	evs, err := sampleEvents(tenantID)
	if err != nil {
		return err
	}
	for _, ev := range evs {
		if err := bus.Publish(ctx, ev); err != nil {
			return fmt.Errorf("publish %s: %w", ev.EventType, err)
		}
	}
	fmt.Fprintf(out, "published %d events\n", len(evs))

	// Read each family's stream back with an ephemeral pull consumer and re-validate.
	v, err := events.NewValidator()
	if err != nil {
		return err
	}
	families := map[string]string{"smtp": "SMTP", "dns": "DNS", "reputation": "REPUTATION", "ai": "AI"}
	got := 0
	for fam, streamName := range families {
		stream, err := bus.JS().Stream(ctx, streamName)
		if err != nil {
			return fmt.Errorf("stream %s: %w", streamName, err)
		}
		cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
			AckPolicy:     jetstream.AckExplicitPolicy,
			FilterSubject: fmt.Sprintf("mxs.%s.>", fam),
		})
		if err != nil {
			return fmt.Errorf("consumer %s: %w", streamName, err)
		}
		batch, err := cons.Fetch(10, jetstream.FetchMaxWait(2*time.Second))
		if err != nil {
			return fmt.Errorf("fetch %s: %w", streamName, err)
		}
		n := 0
		for msg := range batch.Messages() {
			var ev contracts.Envelope
			if err := json.Unmarshal(msg.Data(), &ev); err != nil {
				return fmt.Errorf("decode message on %s: %w", streamName, err)
			}
			if err := v.ValidateRaw(string(ev.EventType), msg.Data()); err != nil {
				return fmt.Errorf("read-back validation failed on %s: %w", streamName, err)
			}
			_ = msg.Ack()
			n++
		}
		if batch.Error() != nil {
			return fmt.Errorf("batch %s: %w", streamName, batch.Error())
		}
		fmt.Fprintf(out, "  %-10s read %d message(s)\n", fam, n)
		got += n
	}
	if got == 0 {
		return fmt.Errorf("selftest read no messages back")
	}
	fmt.Fprintf(out, "bus selftest OK (round-tripped %d message(s))\n", got)
	return nil
}

func sampleEvents(tenantID string) ([]*contracts.Envelope, error) {
	now := time.Now()
	src := "mxctl@selftest"

	smtp, err := events.NewEnvelope(contracts.EventSMTPDelivered, tenantID, src,
		contracts.Correlation{MessageID: "<demo@example.com>", RelayIP: "198.51.100.5", Domain: "example.com"},
		now, contracts.SMTPPayload{
			Outcome: "delivered", Provider: "gmail", RecipientDomain: "gmail.com",
			MessageID: "<demo@example.com>", RelayIP: "198.51.100.5", FromDomain: "example.com",
			SPFResult: "pass", DKIMResult: "pass", DMARCResult: "pass",
			SMTPCode: 250, EnhancedStatus: "2.0.0", DeliveryLatencyMS: 842,
		})
	if err != nil {
		return nil, err
	}

	dns, err := events.NewEnvelope(contracts.EventDNSValidationFailed, tenantID, src,
		contracts.Correlation{Domain: "example.com"},
		now, contracts.DNSPayload{
			Domain: "example.com", DomainID: "11111111-1111-1111-1111-111111111111",
			SnapshotID: "22222222-2222-2222-2222-222222222222",
			Findings: []contracts.DNSFinding{{
				Category: "spf", Severity: "critical", Code: "SPF_LOOKUP_LIMIT",
				Message: "SPF record exceeds 10 DNS lookups (permerror)",
			}},
		})
	if err != nil {
		return nil, err
	}

	rep, err := events.NewEnvelope(contracts.EventReputationBlacklistHit, tenantID, src,
		contracts.Correlation{RelayIP: "198.51.100.5"},
		now, contracts.ReputationPayload{
			Signal: "blacklist_hit", SubjectKind: "ip", Subject: "198.51.100.5",
			Severity: "critical", Source: "zen.spamhaus.org",
		})
	if err != nil {
		return nil, err
	}

	ai, err := events.NewEnvelope(contracts.EventAIRCA, tenantID, src,
		contracts.Correlation{Domain: "example.com"},
		now, contracts.AIPayload{
			Kind: "rca", Title: "DKIM selector missing after DNS change",
			Narrative:  "Microsoft rejections began minutes after the selector2 DKIM record was removed.",
			Confidence: 0.91,
			Impact:     &contracts.AIImpact{AffectedMessages: 37, Provider: "microsoft", Domain: "example.com"},
			Recommendations: []contracts.AIRecommendation{{
				Action: "restore_dkim_selector", Summary: "Restore selector2._domainkey.example.com",
				Target: "selector2._domainkey.example.com", Priority: "urgent",
			}},
			Model: "selftest",
		})
	if err != nil {
		return nil, err
	}

	return []*contracts.Envelope{smtp, dns, rep, ai}, nil
}

func redact(s string) string {
	if s == "" {
		return ""
	}
	return "***redacted***"
}
