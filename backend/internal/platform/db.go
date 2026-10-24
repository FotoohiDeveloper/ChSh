package platform

import (
	"context"
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// NewPool opens a tuned connection pool. Prepared statements are cached per
// connection by pgx, so repeated hot-path queries skip parsing/planning.
func NewPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	if cfg.MaxConns < 16 {
		cfg.MaxConns = 16
	}
	cfg.MinConns = 4
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	cfg.ConnConfig.RuntimeParams["application_name"] = "chador"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "8000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "10000"
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "3000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// Migrate applies embedded *.sql files in order, once each, under an advisory
// lock so that several replicas starting together cannot race.
func Migrate(ctx context.Context, pool *pgxpool.Pool, runtimeRole string) ([]string, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(727001)`); err != nil {
		return nil, err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(727001)`)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, err
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var applied []string
	for _, name := range names {
		var done bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, name).Scan(&done); err != nil {
			return applied, err
		}
		if done {
			continue
		}
		sqlText, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return applied, err
		}
		tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return applied, err
		}
		if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
			_ = tx.Rollback(ctx)
			return applied, fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return applied, err
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, err
		}
		applied = append(applied, name)
	}
	if runtimeRole != "" {
		if err := grantRuntime(ctx, conn, runtimeRole); err != nil {
			return applied, err
		}
	}
	return applied, nil
}

var roleRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// grantRuntime gives the unprivileged runtime role exactly what it needs: DML
// on application tables, and nothing else. In particular it can neither alter
// the schema (so it cannot drop the append-only triggers) nor UPDATE/DELETE the
// audit and event tables. Safe to run repeatedly; it covers tables added by
// future migrations.
func grantRuntime(ctx context.Context, conn *pgxpool.Conn, role string) error {
	if !roleRe.MatchString(role) {
		return fmt.Errorf("invalid runtime role name %q", role)
	}
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, role).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("runtime role %q does not exist; create it before migrating", role)
	}
	q := pgx.Identifier{role}.Sanitize()
	stmts := []string{
		`GRANT USAGE ON SCHEMA public TO ` + q,
		`REVOKE ALL ON ALL TABLES IN SCHEMA public FROM ` + q,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ` + q,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + q,
		`REVOKE ALL ON schema_migrations FROM ` + q,
		`REVOKE UPDATE, DELETE, TRUNCATE ON audit_log, tent_events FROM ` + q,
		`REVOKE CREATE ON SCHEMA public FROM PUBLIC`,
	}
	for _, st := range stmts {
		if _, err := conn.Exec(ctx, st); err != nil {
			return fmt.Errorf("%s: %w", st, err)
		}
	}
	return nil
}
