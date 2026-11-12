// Package pgstore keeps the central server's tokens, sandboxes and usage in
// Postgres. It implements control.TokenStore and control.StateStore.
//
// The schema lives in migrations/, one numbered file per change. Open
// applies the ones a database hasn't seen yet.
package pgstore

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/control"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is a Postgres-backed control.TokenStore and control.StateStore.
type Store struct {
	pool *pgxpool.Pool
}

var (
	_ control.TokenStore = (*Store)(nil)
	_ control.StateStore = (*Store)(nil)
)

// Open connects to the database at url (postgres://...) and brings its
// schema up to date.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close closes the connections.
func (s *Store) Close() { s.pool.Close() }

// migrateLock is the advisory lock that keeps two servers from migrating
// the same database at once. Any fixed number works.
const migrateLock = 7700

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrateLock); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version integer PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		num, _, _ := strings.Cut(base, "_")
		version, err := strconv.Atoi(num)
		if err != nil {
			return fmt.Errorf("migration %s: name must start with a number", base)
		}
		var done bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("migration %s: %w", base, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Check implements control.Tokens.
func (s *Store) Check(ctx context.Context, token string) (control.Principal, bool) {
	if !strings.HasPrefix(token, control.TokenPrefix) {
		return control.Principal{}, false
	}
	var p control.Principal
	err := s.pool.QueryRow(ctx,
		`SELECT name, account_id, max_sandboxes, max_minutes_per_day FROM tokens WHERE sha256 = $1`,
		control.HashToken(token)).Scan(&p.Name, &p.Account, &p.MaxSandboxes, &p.MaxMinutesPerDay)
	return p, err == nil
}

// CreateToken implements control.TokenStore. Names are unique within an
// account.
func (s *Store) CreateToken(ctx context.Context, name, account string, limits control.Limits) (control.TokenRecord, string, error) {
	name, account, err := control.CheckTokenInput(name, account, limits)
	if err != nil {
		return control.TokenRecord{}, "", err
	}
	rec, secret, err := s.createToken(ctx, name, account, "api", limits)
	if isUnique(err) {
		return control.TokenRecord{}, "", fmt.Errorf("a token named %q already exists in account %s", name, account)
	}
	return rec, secret, err
}

func (s *Store) createToken(ctx context.Context, name, account, kind string, limits control.Limits) (control.TokenRecord, string, error) {
	id, secret, err := control.NewTokenSecret()
	if err != nil {
		return control.TokenRecord{}, "", err
	}
	rec := control.TokenRecord{ID: id, Name: name, Account: account, Kind: kind, Hash: control.HashToken(secret),
		Created: time.Now().UTC().Truncate(time.Microsecond), Limits: limits}
	_, err = s.pool.Exec(ctx, `
		WITH a AS (INSERT INTO accounts (id) VALUES ($3) ON CONFLICT DO NOTHING)
		INSERT INTO tokens (id, name, account_id, kind, sha256, max_sandboxes, max_minutes_per_day, created_at)
		VALUES ($1, $2, $3, $8, $4, $5, $6, $7)`,
		rec.ID, rec.Name, rec.Account, rec.Hash, limits.MaxSandboxes, limits.MaxMinutesPerDay, rec.Created, kind)
	if err != nil {
		return control.TokenRecord{}, "", err
	}
	return rec, secret, nil
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// ListTokens implements control.TokenStore.
func (s *Store) ListTokens(ctx context.Context) ([]control.TokenRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+tokenColumns+` FROM tokens ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanToken)
}

const tokenColumns = `id, name, account_id, kind, sha256, max_sandboxes, max_minutes_per_day, created_at`

func scanToken(r pgx.CollectableRow) (control.TokenRecord, error) {
	var t control.TokenRecord
	err := r.Scan(&t.ID, &t.Name, &t.Account, &t.Kind, &t.Hash, &t.MaxSandboxes, &t.MaxMinutesPerDay, &t.Created)
	t.Created = t.Created.UTC()
	return t, err
}

// tokenID finds the token with this id, or the one token with this name.
func (s *Store) tokenID(ctx context.Context, idOrName string) (string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM tokens WHERE id = $1 OR name = $1`, idOrName)
	if err != nil {
		return "", err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("no token %q", idOrName)
	case 1:
		return ids[0], nil
	}
	for _, id := range ids {
		if id == idOrName {
			return id, nil
		}
	}
	return "", fmt.Errorf("%d tokens are named %q, in different accounts; use the id (ephemera-server token ls)", len(ids), idOrName)
}

// SetTokenLimits implements control.TokenStore.
func (s *Store) SetTokenLimits(ctx context.Context, idOrName string, limits control.Limits) error {
	if limits.MaxSandboxes < 0 || limits.MaxMinutesPerDay < 0 {
		return errors.New("limits cannot be negative")
	}
	id, err := s.tokenID(ctx, idOrName)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE tokens SET max_sandboxes = $2, max_minutes_per_day = $3 WHERE id = $1`,
		id, limits.MaxSandboxes, limits.MaxMinutesPerDay)
	return err
}

// RevokeToken implements control.TokenStore.
func (s *Store) RevokeToken(ctx context.Context, idOrName string) error {
	id, err := s.tokenID(ctx, idOrName)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM tokens WHERE id = $1`, id)
	return err
}

// StartSandbox implements control.StateStore.
func (s *Store) StartSandbox(ctx context.Context, sb api.Sandbox) error {
	spec, err := json.Marshal(sb.Spec)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		WITH a AS (INSERT INTO accounts (id) VALUES ($2) ON CONFLICT DO NOTHING)
		INSERT INTO sandboxes (id, account_id, owner, host, host_id, host_url, spec, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		sb.ID, accountOr(sb.Account), sb.Owner, sb.Host, sb.HostID, sb.HostURL, spec, sb.CreatedAt)
	return err
}

func accountOr(a string) string {
	if a == "" {
		return control.DefaultAccount
	}
	return a
}

// EndSandbox implements control.StateStore. The row stays, with when and
// why it ended.
func (s *Store) EndSandbox(ctx context.Context, id string, at time.Time, reason string) error {
	_, err := s.pool.Exec(ctx, `UPDATE sandboxes SET ended_at = $2, end_reason = $3 WHERE id = $1 AND ended_at IS NULL`,
		id, at, reason)
	return err
}

// RunningSandboxes implements control.StateStore.
func (s *Store) RunningSandboxes(ctx context.Context) ([]api.Sandbox, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, account_id, owner, host, host_id, host_url, spec, created_at
		FROM sandboxes WHERE ended_at IS NULL ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (api.Sandbox, error) {
		var sb api.Sandbox
		var spec []byte
		if err := r.Scan(&sb.ID, &sb.Account, &sb.Owner, &sb.Host, &sb.HostID, &sb.HostURL, &spec, &sb.CreatedAt); err != nil {
			return sb, err
		}
		sb.CreatedAt = sb.CreatedAt.UTC()
		return sb, json.Unmarshal(spec, &sb.Spec)
	})
}

// AddUsage implements control.StateStore.
func (s *Store) AddUsage(ctx context.Context, u control.Usage) error {
	_, err := s.pool.Exec(ctx, `
		WITH a AS (INSERT INTO accounts (id) VALUES ($1) ON CONFLICT DO NOTHING)
		INSERT INTO usage (account_id, owner, day, sandbox_seconds) VALUES ($1, $2, $3::date, $4)
		ON CONFLICT (account_id, owner, day) DO UPDATE SET sandbox_seconds = usage.sandbox_seconds + EXCLUDED.sandbox_seconds`,
		accountOr(u.Account), u.Owner, u.Day, u.Seconds)
	return err
}

// UsageSince implements control.StateStore.
func (s *Store) UsageSince(ctx context.Context, day string) ([]control.Usage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT account_id, owner, to_char(day, 'YYYY-MM-DD'), sandbox_seconds
		FROM usage WHERE day >= $1::date ORDER BY day, account_id, owner`, day)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (control.Usage, error) {
		var u control.Usage
		err := r.Scan(&u.Account, &u.Owner, &u.Day, &u.Seconds)
		return u, err
	})
}

var _ control.AccountStore = (*Store)(nil)

// UpsertGitHubAccount implements control.AccountStore.
func (s *Store) UpsertGitHubAccount(ctx context.Context, u control.GitHubUser) (api.Account, error) {
	a := api.Account{ID: control.GitHubAccount(u.ID)}
	var name, avatar *string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO accounts (id, github_id, login, name, avatar_url, last_seen_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), now())
		ON CONFLICT (id) DO UPDATE SET login = EXCLUDED.login, name = EXCLUDED.name,
			avatar_url = EXCLUDED.avatar_url, last_seen_at = now()
		RETURNING login, name, avatar_url`,
		a.ID, u.ID, u.Login, u.Name, u.AvatarURL).Scan(&a.Login, &name, &avatar)
	if name != nil {
		a.Name = *name
	}
	if avatar != nil {
		a.AvatarURL = *avatar
	}
	return a, err
}

// CreateSession implements control.AccountStore. It also clears out
// sessions that have expired.
func (s *Store) CreateSession(ctx context.Context, accountID string, ttl time.Duration) (string, error) {
	secret, err := control.NewSessionSecret()
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`); err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO sessions (sha256, account_id, expires_at) VALUES ($1, $2, $3)`,
		control.HashToken(secret), accountID, time.Now().Add(ttl))
	return secret, err
}

// CheckSession implements control.AccountStore.
func (s *Store) CheckSession(ctx context.Context, secret string) (api.Account, bool) {
	var a api.Account
	var login, name, avatar *string
	err := s.pool.QueryRow(ctx, `
		SELECT a.id, a.login, a.name, a.avatar_url FROM sessions s JOIN accounts a ON a.id = s.account_id
		WHERE s.sha256 = $1 AND s.expires_at > now()`, control.HashToken(secret)).Scan(&a.ID, &login, &name, &avatar)
	for _, f := range []struct {
		dst *string
		src *string
	}{{&a.Login, login}, {&a.Name, name}, {&a.AvatarURL, avatar}} {
		if f.src != nil {
			*f.dst = *f.src
		}
	}
	return a, err == nil
}

// DeleteSession implements control.AccountStore.
func (s *Store) DeleteSession(ctx context.Context, secret string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE sha256 = $1`, control.HashToken(secret))
	return err
}

// AccountTokens implements control.AccountStore.
func (s *Store) AccountTokens(ctx context.Context, accountID string) ([]control.TokenRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+tokenColumns+` FROM tokens WHERE account_id = $1 ORDER BY created_at, id`, accountID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanToken)
}

// RevokeAccountToken implements control.AccountStore.
func (s *Store) RevokeAccountToken(ctx context.Context, accountID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tokens WHERE id = $1 AND account_id = $2`, id, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return control.ErrNotFound
	}
	return nil
}

var _ control.HostStore = (*Store)(nil)

// CreateJoinCode implements control.HostStore. It also clears out codes
// that have expired.
func (s *Store) CreateJoinCode(ctx context.Context, account string, ttl time.Duration) (string, error) {
	account = accountOr(account)
	if !control.ValidAccount(account) {
		return "", fmt.Errorf("account %q: use lowercase letters, digits and dashes, at most 39", account)
	}
	code, err := control.NewJoinCode()
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM join_codes WHERE expires_at < now()`); err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `
		WITH a AS (INSERT INTO accounts (id) VALUES ($2) ON CONFLICT DO NOTHING)
		INSERT INTO join_codes (sha256, account_id, expires_at) VALUES ($1, $2, $3)`,
		control.HashToken(code), account, time.Now().Add(ttl))
	return code, err
}

// JoinHost implements control.HostStore. Using the code and adding the host
// happen in one transaction, so a code works once.
func (s *Store) JoinHost(ctx context.Context, code string, h control.HostRecord) (control.HostRecord, error) {
	if err := control.CheckHost(h); err != nil {
		return control.HostRecord{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return control.HostRecord{}, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `DELETE FROM join_codes WHERE sha256 = $1 AND expires_at > now() RETURNING account_id`,
		control.HashToken(code)).Scan(&h.Account)
	if errors.Is(err, pgx.ErrNoRows) {
		return control.HostRecord{}, control.ErrBadCode
	}
	if err != nil {
		return control.HostRecord{}, err
	}
	h.ID = control.NewHostID()
	err = tx.QueryRow(ctx, `INSERT INTO hosts (id, account_id, name, public_key) VALUES ($1, $2, $3, $4) RETURNING joined_at`,
		h.ID, h.Account, h.Name, h.PublicKey).Scan(&h.Joined)
	if isUnique(err) {
		return control.HostRecord{}, control.ErrNameTaken
	}
	if err != nil {
		return control.HostRecord{}, err
	}
	h.Joined = h.Joined.UTC()
	return h, tx.Commit(ctx)
}

func scanHost(r pgx.CollectableRow) (control.HostRecord, error) {
	var h control.HostRecord
	err := r.Scan(&h.ID, &h.Account, &h.Name, &h.PublicKey, &h.Joined)
	h.Joined = h.Joined.UTC()
	return h, err
}

// Host implements control.HostStore.
func (s *Store) Host(ctx context.Context, id string) (control.HostRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, account_id, name, public_key, joined_at FROM hosts WHERE id = $1`, id)
	if err != nil {
		return control.HostRecord{}, err
	}
	h, err := pgx.CollectExactlyOneRow(rows, scanHost)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, control.ErrNotFound
	}
	return h, err
}

// AccountHosts implements control.HostStore.
func (s *Store) AccountHosts(ctx context.Context, account string) ([]control.HostRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, account_id, name, public_key, joined_at FROM hosts
		WHERE account_id = $1 ORDER BY name, id`, account)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanHost)
}

// RemoveHost implements control.HostStore.
func (s *Store) RemoveHost(ctx context.Context, account, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM hosts WHERE id = $1 AND account_id = $2`, id, account)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return control.ErrNotFound
	}
	return nil
}

var _ control.DeviceStore = (*Store)(nil)

// CreateDeviceLogin implements control.DeviceStore. It also clears out
// sign-ins that have expired.
func (s *Store) CreateDeviceLogin(ctx context.Context, d control.DeviceLogin) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM device_logins WHERE expires_at < $1`, d.Created); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO device_logins (device_hash, user_code, device, status, created_at, expires_at)
		VALUES ($1, $2, $3, 'pending', $4, $5)`, d.DeviceHash, d.UserCode, d.Device, d.Created, d.Expires)
	return err
}

const deviceColumns = `device_hash, user_code, device, coalesce(account_id, ''), status, created_at, expires_at`

func scanDevice(r pgx.CollectableRow) (control.DeviceLogin, error) {
	var d control.DeviceLogin
	err := r.Scan(&d.DeviceHash, &d.UserCode, &d.Device, &d.Account, &d.Status, &d.Created, &d.Expires)
	d.Created, d.Expires = d.Created.UTC(), d.Expires.UTC()
	return d, err
}

func oneDevice(rows pgx.Rows, err error) (control.DeviceLogin, error) {
	if err != nil {
		return control.DeviceLogin{}, err
	}
	d, err := pgx.CollectExactlyOneRow(rows, scanDevice)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, control.ErrNotFound
	}
	return d, err
}

// DeviceLoginByCode implements control.DeviceStore.
func (s *Store) DeviceLoginByCode(ctx context.Context, userCode string, now time.Time) (control.DeviceLogin, error) {
	return oneDevice(s.pool.Query(ctx, `SELECT `+deviceColumns+` FROM device_logins
		WHERE user_code = $1 AND status = 'pending' AND expires_at > $2`, userCode, now))
}

// DecideDeviceLogin implements control.DeviceStore.
func (s *Store) DecideDeviceLogin(ctx context.Context, userCode, accountID string, approve bool, now time.Time) error {
	status := "denied"
	if approve {
		status = "approved"
	}
	tag, err := s.pool.Exec(ctx, `UPDATE device_logins SET status = $2, account_id = $3
		WHERE user_code = $1 AND status = 'pending' AND expires_at > $4`, userCode, status, accountID, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return control.ErrNotFound
	}
	return nil
}

// TakeDeviceLogin implements control.DeviceStore. Deleting a decided
// sign-in and returning it is one statement, so two polls at once can't
// both collect a token.
func (s *Store) TakeDeviceLogin(ctx context.Context, deviceHash string, now time.Time) (control.DeviceLogin, error) {
	d, err := oneDevice(s.pool.Query(ctx, `DELETE FROM device_logins
		WHERE device_hash = $1 AND status <> 'pending' AND expires_at > $2 RETURNING `+deviceColumns, deviceHash, now))
	if !errors.Is(err, control.ErrNotFound) {
		return d, err
	}
	return oneDevice(s.pool.Query(ctx, `SELECT `+deviceColumns+` FROM device_logins
		WHERE device_hash = $1 AND expires_at > $2`, deviceHash, now))
}

// CreateDeviceToken implements control.DeviceStore.
func (s *Store) CreateDeviceToken(ctx context.Context, name, accountID string) (control.TokenRecord, string, error) {
	try := name
	for i := 2; i < 50; i++ {
		rec, secret, err := s.createToken(ctx, try, accountID, "device", control.Limits{})
		if err == nil || !isUnique(err) {
			return rec, secret, err
		}
		try = fmt.Sprintf("%s-%d", name, i)
	}
	return control.TokenRecord{}, "", fmt.Errorf("account %s has too many tokens named %s", accountID, name)
}
