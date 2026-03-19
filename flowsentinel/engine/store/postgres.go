package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPostgresPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	ctxPing, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(ctxPing); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

type Rule struct {
	ID            string
	ClientID      string
	Resource      string
	LimitCount    int
	WindowSeconds int
	UpdatedAt     time.Time
}

func LoadRules(ctx context.Context, pool *pgxpool.Pool) ([]Rule, error) {
	const q = `
SELECT DISTINCT ON (client_id, resource)
  id::text, client_id, resource, limit_count, window_seconds, updated_at
FROM rules
ORDER BY client_id, resource, updated_at DESC;
`
	rows, err := pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.ClientID, &r.Resource, &r.LimitCount, &r.WindowSeconds, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type AuditEvent struct {
	ClientID       string
	Resource       string
	Status         string
	RemainingQuota int64
	CorrelationID  string
}

func InsertAudit(ctx context.Context, pool *pgxpool.Pool, ev AuditEvent) error {
	if ev.ClientID == "" || ev.Resource == "" || ev.Status == "" {
		return errors.New("invalid audit event")
	}
	const q = `
INSERT INTO audit_log (client_id, resource, status, remaining_quota, correlation_id)
VALUES ($1, $2, $3, $4, $5);
`
	ctxIns, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := pool.Exec(ctxIns, q, ev.ClientID, ev.Resource, ev.Status, ev.RemainingQuota, ev.CorrelationID)
	return err
}

func PostgresHealth(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	ctxPing, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var one int
	err := pool.QueryRow(ctxPing, "SELECT 1").Scan(&one)
	if err != nil {
		return false, err
	}
	return true, nil
}

