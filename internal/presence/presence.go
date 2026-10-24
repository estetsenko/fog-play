// Package presence tracks which hosts are online and whether they are idle.
// A host is online only while its heartbeat key has not expired.
package presence

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type Redis struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewRedis(url string, ttl time.Duration) (*Redis, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	return &Redis{rdb: redis.NewClient(opt), ttl: ttl}, nil
}

func key(hostID string) string { return "fogplay:host:" + hostID + ":status" }

// Set records a heartbeat. Status "offline" removes the host immediately.
func (r *Redis) Set(ctx context.Context, hostID, status string) error {
	if status == "offline" {
		return r.rdb.Del(ctx, key(hostID)).Err()
	}
	return r.rdb.Set(ctx, key(hostID), status, r.ttl).Err()
}

// Statuses returns the live status of each host; hosts with no heartbeat are "offline".
func (r *Redis) Statuses(ctx context.Context, hostIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(hostIDs))
	if len(hostIDs) == 0 {
		return out, nil
	}
	keys := make([]string, len(hostIDs))
	for i, id := range hostIDs {
		keys[i] = key(id)
	}
	vals, err := r.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i, id := range hostIDs {
		s, _ := vals[i].(string)
		if s == "" {
			s = "offline"
		}
		out[id] = s
	}
	return out, nil
}

func (r *Redis) Ping(ctx context.Context) error { return r.rdb.Ping(ctx).Err() }

func (r *Redis) Close() error { return r.rdb.Close() }
