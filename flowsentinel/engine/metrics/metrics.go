package metrics

import "github.com/prometheus/client_golang/prometheus"

type Registry struct {
	Registry *prometheus.Registry

	RequestsTotal     *prometheus.CounterVec
	LatencySeconds    prometheus.Histogram
	RedisErrorsTotal  prometheus.Counter
}

func NewRegistry() *Registry {
	reg := prometheus.NewRegistry()

	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "flowsentinel_requests_total",
		Help: "Total number of FlowSentinel /check requests.",
	}, []string{"client_id", "resource", "status"})

	latency := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "flowsentinel_latency_seconds",
		Help:    "Request latency in seconds for FlowSentinel /check.",
		Buckets: prometheus.DefBuckets,
	})

	redisErrors := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "flowsentinel_redis_errors_total",
		Help: "Total number of Redis errors during limiter checks.",
	})

	reg.MustRegister(requests, latency, redisErrors)

	return &Registry{
		Registry:         reg,
		RequestsTotal:    requests,
		LatencySeconds:   latency,
		RedisErrorsTotal: redisErrors,
	}
}

