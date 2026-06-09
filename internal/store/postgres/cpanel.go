package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	cpanelpkg "github.com/zezekim/mxsentinel/internal/cpanel"
)

// ── cPanel Servers ────────────────────────────────────────────────────────────

type CpanelServer struct {
	ID           string
	TenantID     string
	Label        string
	Hostname     string
	Port         int
	Username     string
	APIToken     string
	VerifySSL    bool
	SyncInterval int
	LastSyncedAt *time.Time
	SyncStatus   string
	SyncError    string
	AccountCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (s *Store) ListCpanelServers(ctx context.Context, tenantID string) ([]CpanelServer, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, tenant_id, label, hostname, port, username, api_token,
		        verify_ssl, sync_interval, last_synced_at, sync_status, sync_error,
		        account_count, created_at, updated_at
		 FROM cpanel_servers WHERE tenant_id=$1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CpanelServer
	for rows.Next() {
		var srv CpanelServer
		if err := rows.Scan(&srv.ID, &srv.TenantID, &srv.Label, &srv.Hostname,
			&srv.Port, &srv.Username, &srv.APIToken, &srv.VerifySSL,
			&srv.SyncInterval, &srv.LastSyncedAt, &srv.SyncStatus, &srv.SyncError,
			&srv.AccountCount, &srv.CreatedAt, &srv.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

func (s *Store) CreateCpanelServer(ctx context.Context, tenantID, label, hostname string, port int, username, encryptedToken string, verifySSL bool, syncInterval int) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx,
		`INSERT INTO cpanel_servers (tenant_id, label, hostname, port, username, api_token, verify_ssl, sync_interval)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
		tenantID, label, hostname, port, username, encryptedToken, verifySSL, syncInterval,
	).Scan(&id)
	return id, err
}

func (s *Store) GetCpanelServer(ctx context.Context, tenantID, id string) (CpanelServer, error) {
	var srv CpanelServer
	err := s.Pool.QueryRow(ctx,
		`SELECT id, tenant_id, label, hostname, port, username, api_token,
		        verify_ssl, sync_interval, last_synced_at, sync_status, sync_error,
		        account_count, created_at, updated_at
		 FROM cpanel_servers WHERE id=$1 AND tenant_id=$2`, id, tenantID,
	).Scan(&srv.ID, &srv.TenantID, &srv.Label, &srv.Hostname,
		&srv.Port, &srv.Username, &srv.APIToken, &srv.VerifySSL,
		&srv.SyncInterval, &srv.LastSyncedAt, &srv.SyncStatus, &srv.SyncError,
		&srv.AccountCount, &srv.CreatedAt, &srv.UpdatedAt)
	if err == pgx.ErrNoRows {
		return srv, pgx.ErrNoRows
	}
	return srv, err
}

func (s *Store) UpdateCpanelServer(ctx context.Context, tenantID, id, label string, verifySSL bool, syncInterval int) (bool, error) {
	tag, err := s.Pool.Exec(ctx,
		`UPDATE cpanel_servers SET label=$1, verify_ssl=$2, sync_interval=$3, updated_at=now()
		 WHERE id=$4 AND tenant_id=$5`, label, verifySSL, syncInterval, id, tenantID)
	return tag.RowsAffected() > 0, err
}

func (s *Store) DeleteCpanelServer(ctx context.Context, tenantID, id string) (bool, error) {
	tag, err := s.Pool.Exec(ctx,
		`DELETE FROM cpanel_servers WHERE id=$1 AND tenant_id=$2`, id, tenantID)
	return tag.RowsAffected() > 0, err
}

func (s *Store) SetServerSyncResult(ctx context.Context, serverID, status, errMsg string, accountCount int) error {
	var lastSynced interface{}
	if status == "ok" || status == "error" {
		lastSynced = "now()"
	}
	var err error
	if lastSynced != nil {
		_, err = s.Pool.Exec(ctx,
			`UPDATE cpanel_servers SET sync_status=$1, sync_error=$2, account_count=$3,
			        last_synced_at=now(), updated_at=now()
			 WHERE id=$4`, status, errMsg, accountCount, serverID)
	} else {
		_, err = s.Pool.Exec(ctx,
			`UPDATE cpanel_servers SET sync_status=$1, sync_error=$2, updated_at=now()
			 WHERE id=$3`, status, errMsg, serverID)
	}
	return err
}

func (s *Store) GetServersNeedingSync(ctx context.Context) ([]CpanelServer, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, tenant_id, label, hostname, port, username, api_token,
		        verify_ssl, sync_interval, last_synced_at, sync_status, sync_error,
		        account_count, created_at, updated_at
		 FROM cpanel_servers
		 WHERE last_synced_at IS NULL
		    OR last_synced_at < now() - (sync_interval::text || ' seconds')::interval
		 ORDER BY last_synced_at ASC NULLS FIRST`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CpanelServer
	for rows.Next() {
		var srv CpanelServer
		if err := rows.Scan(&srv.ID, &srv.TenantID, &srv.Label, &srv.Hostname,
			&srv.Port, &srv.Username, &srv.APIToken, &srv.VerifySSL,
			&srv.SyncInterval, &srv.LastSyncedAt, &srv.SyncStatus, &srv.SyncError,
			&srv.AccountCount, &srv.CreatedAt, &srv.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

// ── cPanel Accounts ───────────────────────────────────────────────────────────

type CpanelAccount struct {
	ID            string
	ServerID      string
	Username      string
	PrimaryDomain string
	OwnerEmail    string
	Plan          string
	Suspended     bool
	DiskUsedMB    int
	SyncedAt      time.Time
}

func (s *Store) ListCpanelAccounts(ctx context.Context, serverID string) ([]CpanelAccount, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, server_id, username, primary_domain, owner_email, plan, suspended, disk_used_mb, synced_at
		 FROM cpanel_accounts WHERE server_id=$1 ORDER BY primary_domain ASC`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CpanelAccount
	for rows.Next() {
		var a CpanelAccount
		if err := rows.Scan(&a.ID, &a.ServerID, &a.Username, &a.PrimaryDomain,
			&a.OwnerEmail, &a.Plan, &a.Suspended, &a.DiskUsedMB, &a.SyncedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetCpanelAccountByUsername(ctx context.Context, serverID, username string) (CpanelAccount, error) {
	var a CpanelAccount
	err := s.Pool.QueryRow(ctx,
		`SELECT id, server_id, username, primary_domain, owner_email, plan, suspended, disk_used_mb, synced_at
		 FROM cpanel_accounts WHERE server_id=$1 AND username=$2`, serverID, username,
	).Scan(&a.ID, &a.ServerID, &a.Username, &a.PrimaryDomain,
		&a.OwnerEmail, &a.Plan, &a.Suspended, &a.DiskUsedMB, &a.SyncedAt)
	return a, err
}

func (s *Store) UpsertCpanelAccount(ctx context.Context, serverID string, acc cpanelpkg.Account) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx,
		`INSERT INTO cpanel_accounts (server_id, username, primary_domain, owner_email, plan, suspended, disk_used_mb, synced_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		 ON CONFLICT (server_id, username) DO UPDATE SET
		   primary_domain=EXCLUDED.primary_domain, owner_email=EXCLUDED.owner_email,
		   plan=EXCLUDED.plan, suspended=EXCLUDED.suspended, disk_used_mb=EXCLUDED.disk_used_mb,
		   synced_at=now()
		 RETURNING id`,
		serverID, acc.Username, acc.PrimaryDomain, acc.OwnerEmail,
		acc.Plan, acc.Suspended, acc.DiskUsedMB,
	).Scan(&id)
	return id, err
}

func (s *Store) DeleteStaleAccounts(ctx context.Context, serverID string, seenUsernames []string) error {
	_, err := s.Pool.Exec(ctx,
		`DELETE FROM cpanel_accounts WHERE server_id=$1 AND username <> ALL($2)`,
		serverID, seenUsernames)
	return err
}

// ── cPanel Domains ────────────────────────────────────────────────────────────

type CpanelDomain struct {
	ID         string
	AccountID  string
	Domain     string
	DomainType string
	CreatedAt  time.Time
}

func (s *Store) ListAccountDomains(ctx context.Context, accountID string) ([]CpanelDomain, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, account_id, domain, domain_type, created_at
		 FROM cpanel_domains WHERE account_id=$1 ORDER BY domain_type, domain`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CpanelDomain
	for rows.Next() {
		var d CpanelDomain
		if err := rows.Scan(&d.ID, &d.AccountID, &d.Domain, &d.DomainType, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) UpsertCpanelDomain(ctx context.Context, accountID string, d cpanelpkg.Domain) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO cpanel_domains (account_id, domain, domain_type)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (account_id, domain) DO UPDATE SET domain_type=EXCLUDED.domain_type`,
		accountID, d.Domain, d.Type)
	return err
}

func (s *Store) DeleteStaleDomains(ctx context.Context, accountID string, seenDomains []string) error {
	_, err := s.Pool.Exec(ctx,
		`DELETE FROM cpanel_domains WHERE account_id=$1 AND domain <> ALL($2)`,
		accountID, seenDomains)
	return err
}

func (s *Store) LookupDomainAccount(ctx context.Context, domain string) (CpanelAccount, error) {
	var a CpanelAccount
	err := s.Pool.QueryRow(ctx,
		`SELECT ca.id, ca.server_id, ca.username, ca.primary_domain, ca.owner_email, ca.plan, ca.suspended, ca.disk_used_mb, ca.synced_at
		 FROM cpanel_accounts ca
		 JOIN cpanel_domains cd ON cd.account_id = ca.id
		 WHERE cd.domain = $1
		 LIMIT 1`, domain,
	).Scan(&a.ID, &a.ServerID, &a.Username, &a.PrimaryDomain,
		&a.OwnerEmail, &a.Plan, &a.Suspended, &a.DiskUsedMB, &a.SyncedAt)
	return a, err
}

func (s *Store) GetDomainMap(ctx context.Context, serverID string) (map[string]CpanelAccount, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT cd.domain, ca.id, ca.server_id, ca.username, ca.primary_domain, ca.owner_email, ca.plan, ca.suspended, ca.disk_used_mb, ca.synced_at
		 FROM cpanel_domains cd
		 JOIN cpanel_accounts ca ON ca.id = cd.account_id
		 WHERE ca.server_id = $1`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]CpanelAccount{}
	for rows.Next() {
		var domain string
		var a CpanelAccount
		if err := rows.Scan(&domain, &a.ID, &a.ServerID, &a.Username, &a.PrimaryDomain,
			&a.OwnerEmail, &a.Plan, &a.Suspended, &a.DiskUsedMB, &a.SyncedAt); err != nil {
			return nil, err
		}
		m[domain] = a
	}
	return m, rows.Err()
}

// ── WHMCS Connections ─────────────────────────────────────────────────────────

type WhmcsConnection struct {
	ID               string
	TenantID         string
	Label            string
	APIURL           string
	APIIdentifier    string
	APISecret        string
	PushFrequency    string
	PushMetricFields []string
	Enabled          bool
	LastPushedAt     *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type WhmcsPushLog struct {
	ID             string
	ConnectionID   string
	PushedAt       time.Time
	AccountsPushed int
	Status         string
	ErrorDetail    string
	PeriodStart    time.Time
	PeriodEnd      time.Time
}

func (s *Store) ListWhmcsConnections(ctx context.Context, tenantID string) ([]WhmcsConnection, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, tenant_id, label, api_url, api_identifier, api_secret,
		        push_frequency, push_metric_fields, enabled, last_pushed_at, created_at, updated_at
		 FROM whmcs_connections WHERE tenant_id=$1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WhmcsConnection
	for rows.Next() {
		var c WhmcsConnection
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Label, &c.APIURL, &c.APIIdentifier,
			&c.APISecret, &c.PushFrequency, &c.PushMetricFields, &c.Enabled,
			&c.LastPushedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CreateWhmcsConnection(ctx context.Context, tenantID, label, apiURL, apiIdentifier, encryptedSecret, pushFrequency string, pushMetricFields []string) (string, error) {
	if len(pushMetricFields) == 0 {
		pushMetricFields = []string{"delivered", "bounced", "rejected", "total"}
	}
	var id string
	err := s.Pool.QueryRow(ctx,
		`INSERT INTO whmcs_connections (tenant_id, label, api_url, api_identifier, api_secret, push_frequency, push_metric_fields)
		 VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		tenantID, label, apiURL, apiIdentifier, encryptedSecret, pushFrequency, pushMetricFields,
	).Scan(&id)
	return id, err
}

func (s *Store) GetWhmcsConnection(ctx context.Context, tenantID, id string) (WhmcsConnection, error) {
	var c WhmcsConnection
	err := s.Pool.QueryRow(ctx,
		`SELECT id, tenant_id, label, api_url, api_identifier, api_secret,
		        push_frequency, push_metric_fields, enabled, last_pushed_at, created_at, updated_at
		 FROM whmcs_connections WHERE id=$1 AND tenant_id=$2`, id, tenantID,
	).Scan(&c.ID, &c.TenantID, &c.Label, &c.APIURL, &c.APIIdentifier,
		&c.APISecret, &c.PushFrequency, &c.PushMetricFields, &c.Enabled,
		&c.LastPushedAt, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (s *Store) UpdateWhmcsConnection(ctx context.Context, tenantID, id, label, pushFrequency string, pushMetricFields []string, enabled bool) (bool, error) {
	tag, err := s.Pool.Exec(ctx,
		`UPDATE whmcs_connections SET label=$1, push_frequency=$2, push_metric_fields=$3, enabled=$4, updated_at=now()
		 WHERE id=$5 AND tenant_id=$6`, label, pushFrequency, pushMetricFields, enabled, id, tenantID)
	return tag.RowsAffected() > 0, err
}

func (s *Store) DeleteWhmcsConnection(ctx context.Context, tenantID, id string) (bool, error) {
	tag, err := s.Pool.Exec(ctx,
		`DELETE FROM whmcs_connections WHERE id=$1 AND tenant_id=$2`, id, tenantID)
	return tag.RowsAffected() > 0, err
}

func (s *Store) RecordWhmcsPush(ctx context.Context, connectionID string, accountsPushed int, status, errorDetail string, periodStart, periodEnd time.Time) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO whmcs_push_log (connection_id, accounts_pushed, status, error_detail, period_start, period_end)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		connectionID, accountsPushed, status, errorDetail, periodStart, periodEnd)
	return err
}

func (s *Store) ListWhmcsPushLog(ctx context.Context, connectionID string, limit int) ([]WhmcsPushLog, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, connection_id, pushed_at, accounts_pushed, status, COALESCE(error_detail,''), period_start, period_end
		 FROM whmcs_push_log WHERE connection_id=$1
		 ORDER BY pushed_at DESC LIMIT $2`, connectionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WhmcsPushLog
	for rows.Next() {
		var l WhmcsPushLog
		if err := rows.Scan(&l.ID, &l.ConnectionID, &l.PushedAt, &l.AccountsPushed,
			&l.Status, &l.ErrorDetail, &l.PeriodStart, &l.PeriodEnd); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) GetDueWhmcsConnections(ctx context.Context) ([]WhmcsConnection, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, tenant_id, label, api_url, api_identifier, api_secret,
		        push_frequency, push_metric_fields, enabled, last_pushed_at, created_at, updated_at
		 FROM whmcs_connections
		 WHERE enabled = true
		   AND (last_pushed_at IS NULL
		     OR (push_frequency = 'daily'  AND last_pushed_at < now() - interval '24 hours')
		     OR (push_frequency = 'weekly' AND last_pushed_at < now() - interval '7 days'))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WhmcsConnection
	for rows.Next() {
		var c WhmcsConnection
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Label, &c.APIURL, &c.APIIdentifier,
			&c.APISecret, &c.PushFrequency, &c.PushMetricFields, &c.Enabled,
			&c.LastPushedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) MarkWhmcsPushed(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE whmcs_connections SET last_pushed_at=now(), updated_at=now() WHERE id=$1`, id)
	return err
}

// GetTenantCpanelServers returns all cPanel servers for a tenant (used by cpaneld for WHMCS push).
func (s *Store) GetTenantCpanelServers(ctx context.Context, tenantID string) ([]CpanelServer, error) {
	return s.ListCpanelServers(ctx, tenantID)
}

// GetAllDomainMaps returns domain->account maps for all servers, keyed by serverID.
func (s *Store) GetAllDomainMaps(ctx context.Context, tenantID string) (map[string]map[string]CpanelAccount, error) {
	servers, err := s.ListCpanelServers(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get all domain maps: %w", err)
	}
	result := make(map[string]map[string]CpanelAccount, len(servers))
	for _, srv := range servers {
		dm, err := s.GetDomainMap(ctx, srv.ID)
		if err != nil {
			return nil, fmt.Errorf("get domain map for server %s: %w", srv.ID, err)
		}
		result[srv.ID] = dm
	}
	return result, nil
}
