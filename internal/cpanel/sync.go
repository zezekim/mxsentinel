package cpanel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// SyncStore is the persistence interface used by Syncer.
type SyncStore interface {
	UpsertCpanelAccount(ctx context.Context, serverID string, acc Account) (string, error)
	UpsertCpanelDomain(ctx context.Context, accountID string, d Domain) error
	DeleteStaleAccounts(ctx context.Context, serverID string, seenUsernames []string) error
	DeleteStaleDomains(ctx context.Context, accountID string, seenDomains []string) error
	SetServerSyncResult(ctx context.Context, serverID, status, errMsg string, accountCount int) error
}

// Syncer performs a full cPanel account+domain sync for one WHM server.
type Syncer struct {
	client *Client
	store  SyncStore
	log    *slog.Logger
}

// NewSyncer creates a Syncer.
func NewSyncer(client *Client, store SyncStore, log *slog.Logger) *Syncer {
	return &Syncer{client: client, store: store, log: log}
}

// Run performs a full sync: list all accounts, for each account list all domains,
// upsert everything, then delete stale records. Updates sync_status via SetServerSyncResult.
func (s *Syncer) Run(ctx context.Context, serverID string) error {
	if err := s.store.SetServerSyncResult(ctx, serverID, "syncing", "", 0); err != nil {
		s.log.Warn("cpanel: could not set sync status to syncing", "server_id", serverID, "err", err)
	}

	accts, err := s.client.ListAccounts(ctx)
	if err != nil {
		_ = s.store.SetServerSyncResult(ctx, serverID, "error", err.Error(), 0)
		return fmt.Errorf("cpanel sync %s: list accounts: %w", serverID, err)
	}

	s.log.Info("cpanel sync: accounts fetched", "server_id", serverID, "count", len(accts))

	var errs []string
	seenUsernames := make([]string, 0, len(accts))
	totalDomains := 0

	for _, acct := range accts {
		seenUsernames = append(seenUsernames, acct.Username)

		accountID, err := s.store.UpsertCpanelAccount(ctx, serverID, acct)
		if err != nil {
			errs = append(errs, fmt.Sprintf("upsert account %s: %v", acct.Username, err))
			continue
		}

		domains, err := s.client.ListDomains(ctx, acct.Username, acct.PrimaryDomain)
		if err != nil {
			s.log.Warn("cpanel: failed to list domains for account", "username", acct.Username, "err", err)
			domains = []Domain{{Domain: strings.ToLower(acct.PrimaryDomain), Type: "main"}}
		}

		seenDomains := make([]string, 0, len(domains))
		for _, d := range domains {
			seenDomains = append(seenDomains, strings.ToLower(d.Domain))
			if err := s.store.UpsertCpanelDomain(ctx, accountID, d); err != nil {
				errs = append(errs, fmt.Sprintf("upsert domain %s: %v", d.Domain, err))
			}
		}
		totalDomains += len(domains)

		if err := s.store.DeleteStaleDomains(ctx, accountID, seenDomains); err != nil {
			s.log.Warn("cpanel: delete stale domains", "account_id", accountID, "err", err)
		}
	}

	if err := s.store.DeleteStaleAccounts(ctx, serverID, seenUsernames); err != nil {
		s.log.Warn("cpanel: delete stale accounts", "server_id", serverID, "err", err)
	}

	s.log.Info("cpanel sync complete", "server_id", serverID, "accounts", len(accts), "domains", totalDomains)

	status := "ok"
	errMsg := ""
	if len(errs) > 0 {
		status = "error"
		errMsg = strings.Join(errs, "; ")
	}
	return s.store.SetServerSyncResult(ctx, serverID, status, errMsg, len(accts))
}
