package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr      string
	DatabaseURL   string
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	CacheTTL      time.Duration
}

func Load() Config {
	return Config{
		HTTPAddr:      getenv("HTTP_ADDR", ":8080"),
		DatabaseURL:   getenv("DATABASE_URL", "postgres://mapcluster:mapcluster@postgres:5432/mapcluster?sslmode=disable"),
		RedisAddr:     getenv("REDIS_ADDR", "redis:6379"),
		RedisPassword: getenv("REDIS_PASSWORD", ""),
		RedisDB:       getenvInt("REDIS_DB", 0),
		CacheTTL:      getenvDuration("CACHE_TTL", 30*time.Second),
	}
}

func getenv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed
		}
	}
	return fallback
}
