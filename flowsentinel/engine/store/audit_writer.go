package store

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type AuditWriter struct {
	logger *slog.Logger
	pool   *pgxpool.Pool
	ch     chan AuditEvent
}

func NewAuditWriter(logger *slog.Logger, pool *pgxpool.Pool) *AuditWriter {
	return &AuditWriter{
		logger: logger,
		pool:   pool,
		ch:     make(chan AuditEvent, 1024),
	}
}

func (w *AuditWriter) Pool() *pgxpool.Pool { return w.pool }

func (w *AuditWriter) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-w.ch:
				if err := InsertAudit(ctx, w.pool, ev); err != nil {
					w.logger.Error("audit_insert_error",
						"error", err.Error(),
						"client_id", ev.ClientID,
						"resource", ev.Resource,
						"status", ev.Status,
						"correlation_id", ev.CorrelationID,
					)
				}
			}
		}
	}()
}

// Insert enqueues an audit write. If the queue is saturated, it explicitly logs and attempts a direct write.
func (w *AuditWriter) Insert(ctx context.Context, ev AuditEvent) error {
	select {
	case w.ch <- ev:
		return nil
	default:
		w.logger.Error("audit_queue_full",
			"client_id", ev.ClientID,
			"resource", ev.Resource,
			"status", ev.Status,
			"correlation_id", ev.CorrelationID,
		)
		ctxIns, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return InsertAudit(ctxIns, w.pool, ev)
	}
}

