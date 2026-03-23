package limiter

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

const luaSlidingWindow = `
local current = redis.call("INCR", KEYS[1])
if current == 1 then
  redis.call("EXPIRE", KEYS[1], ARGV[1])
end
return current
`

type RedisSlidingWindow struct {
	client          *redis.Client
	script          *redis.Script
	redisErrorsTotal prometheus.Counter
}

func NewRedisSlidingWindow(client *redis.Client, redisErrorsTotal prometheus.Counter) *RedisSlidingWindow {
	return &RedisSlidingWindow{
		client:          client,
		script:          redis.NewScript(luaSlidingWindow),
		redisErrorsTotal: redisErrorsTotal,
	}
}

func (l *RedisSlidingWindow) Redis() *redis.Client { return l.client }

// Check implements the required Redis approach:
// Checks approved
// Key: flowsentinel:{client_id}:{resource}:{window_bucket}
// - INCR counter
// - EXPIRE if new key chnages
// - Return current count vs limit (caller compares)
func (l *RedisSlidingWindow) Check(ctx context.Context, clientID, resource string, limit, windowSeconds int64) (count int64, resetTimeUnix int64, err error) {
	if windowSeconds <= 0 {
		return 0, 0, fmt.Errorf("window_seconds must be > 0")
	}

	now := time.Now().Unix()
	bucket := now / windowSeconds
	key := fmt.Sprintf("flowsentinel:%s:%s:%d", clientID, resource, bucket)
	resetTimeUnix = (bucket + 1) * windowSeconds

	res, err := l.script.Run(ctx, l.client, []string{key}, windowSeconds).Int64()
	if err != nil {
		l.redisErrorsTotal.Inc()
		return 0, 0, err
	}
	return res, resetTimeUnix, nil
}
