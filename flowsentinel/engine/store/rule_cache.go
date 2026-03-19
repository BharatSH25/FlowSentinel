package store

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type rulesSnapshot map[string]map[string]Rule // client_id -> resource -> rule

type RuleCache struct {
	logger   *slog.Logger
	pool     *pgxpool.Pool
	interval time.Duration
	snap     atomic.Value // rulesSnapshot
}

func NewRuleCache(logger *slog.Logger, pool *pgxpool.Pool, interval time.Duration) *RuleCache {
	rc := &RuleCache{logger: logger, pool: pool, interval: interval}
	rc.snap.Store(rulesSnapshot{})
	return rc
}

func (c *RuleCache) Start(ctx context.Context) error {
	if err := c.refresh(ctx); err != nil {
		return err
	}
	go c.loop(ctx)
	return nil
}

func (c *RuleCache) loop(ctx context.Context) {
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.refresh(ctx); err != nil {
				c.logger.Error("rules_refresh_error", "error", err.Error())
			}
		}
	}
}

func (c *RuleCache) refresh(ctx context.Context) error {
	ctxLoad, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rules, err := LoadRules(ctxLoad, c.pool)
	if err != nil {
		return err
	}
	next := rulesSnapshot{}
	for _, r := range rules {
		if _, ok := next[r.ClientID]; !ok {
			next[r.ClientID] = map[string]Rule{}
		}
		next[r.ClientID][r.Resource] = r
	}
	c.snap.Store(next)
	c.logger.Info("rules_refreshed", "count", len(rules))
	return nil
}

func (c *RuleCache) Get(clientID, resource string) (Rule, bool) {
	s := c.snap.Load().(rulesSnapshot)
	rm, ok := s[clientID]
	if !ok {
		return Rule{}, false
	}
	r, ok := rm[resource]
	return r, ok
}

