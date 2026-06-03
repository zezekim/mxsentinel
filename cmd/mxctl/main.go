// Command mxctl is the MX Sentinel operator CLI: run database migrations, set up the
// event bus, seed local data, and smoke-test the bus end to end.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"

	"github.com/zezekim/mxsentinel/internal/api"
	"github.com/zezekim/mxsentinel/internal/auth"
	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/obs"
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
		userCmd(loadCfg),
		ipPoolCmd(loadCfg),
		relayNodeCmd(loadCfg),
	)
	return root
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

	c.AddCommand(create)
	return c
}

func apikeyCmd(load func() (config.Config, error)) *cobra.Command {
	c := &cobra.Command{Use: "apikey", Short: "Manage API credentials"}

	var tenantSlug, name, scopes string
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
			token, prefix, hash, err := api.GenerateToken()
			if err != nil {
				return err
			}
			id, err := pg.CreateAPICredential(ctx, tenant.ID, name, prefix, hash, scopeList)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "created api credential %s for tenant %s (scopes: %v)\n", id, tenantSlug, scopeList)
			fmt.Fprintf(out, "token (shown once, store it now):\n  %s\n", token)
			return nil
		},
	}
	create.Flags().StringVar(&tenantSlug, "tenant", "", "tenant slug (e.g. demo)")
	create.Flags().StringVar(&name, "name", "dashboard", "credential name")
	create.Flags().StringVar(&scopes, "scopes", "read", "comma-separated scopes: read,write,admin")

	c.AddCommand(create)
	return c
}

// parseScopes splits/validates a comma-separated scope list (read|write|admin).
func parseScopes(s string) ([]string, error) {
	valid := map[string]bool{api.ScopeRead: true, api.ScopeWrite: true, api.ScopeAdmin: true}
	seen := map[string]bool{}
	var out []string
	for _, raw := range strings.Split(s, ",") {
		sc := strings.TrimSpace(strings.ToLower(raw))
		if sc == "" {
			continue
		}
		if !valid[sc] {
			return nil, fmt.Errorf("invalid scope %q (allowed: read, write, admin)", sc)
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
