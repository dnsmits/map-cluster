package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisCache struct {
	client *redis.Client
}

func NewRedisClient(addr, password string, db int) *RedisCache {
	return &RedisCache{client: redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})}
}

func (r *RedisCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	value, err := r.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func (r *RedisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}

func (r *RedisCache) DeleteByPrefix(ctx context.Context, prefix string) error {
	var cursor uint64
	pattern := prefix + "*"

	for {
		keys, nextCursor, err := r.client.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := r.client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return nil
}

func (r *RedisCache) Close() error {
	return r.client.Close()
}
