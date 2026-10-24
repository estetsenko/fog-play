// Package config loads service settings from environment variables.
package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	Addr        string
	DatabaseURL string
	RedisURL    string
	JWTSecret   []byte
	TokenTTL    time.Duration
	// HeartbeatTTL is how long a host stays "online" after its last heartbeat.
	HeartbeatTTL time.Duration
}

func Load() (Config, error) {
	c := Config{
		Addr:         getenv("FOGPLAY_ADDR", ":8080"),
		DatabaseURL:  os.Getenv("DATABASE_URL"),
		RedisURL:     getenv("REDIS_URL", "redis://localhost:6379/0"),
		JWTSecret:    []byte(os.Getenv("JWT_SECRET")),
		TokenTTL:     24 * time.Hour,
		HeartbeatTTL: 30 * time.Second,
	}
	if v := os.Getenv("HEARTBEAT_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("HEARTBEAT_TTL: %w", err)
		}
		c.HeartbeatTTL = d
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if len(c.JWTSecret) < 32 {
		return c, fmt.Errorf("JWT_SECRET must be at least 32 bytes")
	}
	return c, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
