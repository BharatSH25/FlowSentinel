package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"flowsentinel/engine/limiter"
	"flowsentinel/engine/metrics"
	"flowsentinel/engine/store"
)

type Handler struct {
	logger      *slog.Logger
	rules       *store.RuleCache
	limiter     *limiter.RedisSlidingWindow
	auditWriter *store.AuditWriter
	metrics     *metrics.Registry
}

func New(logger *slog.Logger, rules *store.RuleCache, lim *limiter.RedisSlidingWindow, auditWriter *store.AuditWriter, reg *metrics.Registry) *Handler {
	return &Handler{
		logger:      logger,
		rules:       rules,
		limiter:     lim,
		auditWriter: auditWriter,
		metrics:     reg,
	}
}

type checkRequest struct {
	ClientID string `json:"client_id"`
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

type checkResponse struct {
	Allowed        bool  `json:"allowed"`
	RemainingQuota int64 `json:"remaining_quota"`
	ResetTime      int64 `json:"reset_time"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) Check(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	cid := CorrelationIDFromContext(r.Context())

	var req checkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.metrics.RequestsTotal.WithLabelValues("unknown", "unknown", "denied").Inc()
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_json"})
		return
	}
	if req.ClientID == "" || req.Resource == "" || req.Action == "" {
		h.metrics.RequestsTotal.WithLabelValues(req.ClientID, req.Resource, "denied").Inc()
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "client_id, resource, action are required"})
		return
	}

	rule, ok := h.rules.Get(req.ClientID, req.Resource)
	if !ok {
		h.metrics.RequestsTotal.WithLabelValues(req.ClientID, req.Resource, "allowed").Inc()
		h.metrics.LatencySeconds.Observe(time.Since(start).Seconds())
		resp := checkResponse{Allowed: true, RemainingQuota: -1, ResetTime: 0}
		if err := h.auditWriter.Insert(r.Context(), store.AuditEvent{
			ClientID:       req.ClientID,
			Resource:       req.Resource,
			Status:         "allowed",
			RemainingQuota: resp.RemainingQuota,
			CorrelationID:  cid,
		}); err != nil {
			h.logger.Error("audit_write_error", "error", err.Error(), "correlation_id", cid)
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	count, resetTime, err := h.limiter.Check(r.Context(), req.ClientID, req.Resource, int64(rule.LimitCount), int64(rule.WindowSeconds))
	if err != nil {
		h.logger.Error("redis_check_error", "error", err.Error(), "client_id", req.ClientID, "resource", req.Resource, "correlation_id", cid)
		h.metrics.RequestsTotal.WithLabelValues(req.ClientID, req.Resource, "denied").Inc()
		h.metrics.LatencySeconds.Observe(time.Since(start).Seconds())
		resp := checkResponse{Allowed: false, RemainingQuota: 0, ResetTime: 0}
		if err := h.auditWriter.Insert(r.Context(), store.AuditEvent{
			ClientID:       req.ClientID,
			Resource:       req.Resource,
			Status:         "denied",
			RemainingQuota: resp.RemainingQuota,
			CorrelationID:  cid,
		}); err != nil {
			h.logger.Error("audit_write_error", "error", err.Error(), "correlation_id", cid)
		}
		writeJSON(w, http.StatusServiceUnavailable, resp)
		return
	}

	allowed := count <= int64(rule.LimitCount)
	remaining := int64(rule.LimitCount) - count
	if remaining < 0 {
		remaining = 0
	}
	status := "denied"
	if allowed {
		status = "allowed"
	}

	h.metrics.RequestsTotal.WithLabelValues(req.ClientID, req.Resource, status).Inc()
	h.metrics.LatencySeconds.Observe(time.Since(start).Seconds())

	resp := checkResponse{
		Allowed:        allowed,
		RemainingQuota: remaining,
		ResetTime:      resetTime,
	}

	if err := h.auditWriter.Insert(r.Context(), store.AuditEvent{
		ClientID:       req.ClientID,
		Resource:       req.Resource,
		Status:         status,
		RemainingQuota: remaining,
		CorrelationID:  cid,
	}); err != nil {
		h.logger.Error("audit_write_error", "error", err.Error(), "correlation_id", cid)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	cid := CorrelationIDFromContext(r.Context())
	redisOK, redisErr := store.RedisHealth(r.Context(), h.limiter.Redis())
	pgOK, pgErr := store.PostgresHealth(r.Context(), h.auditWriter.Pool())

	resp := map[string]any{
		"status":         "ok",
		"redis":          map[string]any{"ok": redisOK},
		"postgres":       map[string]any{"ok": pgOK},
		"correlation_id": cid,
	}
	if redisErr != nil {
		resp["redis"].(map[string]any)["error"] = redisErr.Error()
	}
	if pgErr != nil {
		resp["postgres"].(map[string]any)["error"] = pgErr.Error()
	}

	if !redisOK || !pgOK {
		resp["status"] = "degraded"
		writeJSON(w, http.StatusServiceUnavailable, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
