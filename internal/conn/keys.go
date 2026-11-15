package conn

// Direct reads of asynq's own Redis keys.
//
// This file is the one place asynqui assumes asynq's key layout, for the
// time sort of the scheduled, retry, archived and completed tables: one
// range read of a page of IDs, each then hydrated with the public
// Inspector.GetTaskInfo. Only plain read-only range reads belong here: no
// writes, Lua, scans or client-side sorting.
//
// Checked against hibiken/asynq v0.26.0 internal/base and internal/rdb.
// TestIntegrationKeys pins every key and score below; re-check both when
// bumping asynq.
//
// Keys read (sorted sets, ZRANGE / ZREVRANGE ... WITHSCORES), for queue q:
//
//	asynq:{q}:scheduled  base.ScheduledKey. Score: process_at (Unix s).
//	asynq:{q}:retry      base.RetryKey. Score: next process_at (Unix s).
//	asynq:{q}:archived   base.ArchivedKey. Score: when the task was
//	                     archived (Unix s), by a worker or the Inspector.
//	asynq:{q}:completed  base.CompletedKey. Score: completed_at +
//	                     retention, the expiry (Unix s).

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// asynqQueuePrefix is base.QueueKeyPrefix (asynq v0.26.0
// internal/base/base.go): every key of a queue starts with it. The braces
// are a Redis Cluster hash tag.
func asynqQueuePrefix(queue string) string { return "asynq:{" + queue + "}:" }

// stateKey is the sorted set holding the IDs of the queue's tasks in
// state, scored by the state's time. Other states (active, pending,
// aggregating) are not read directly and have none.
func stateKey(queue string, state TaskState) (string, bool) {
	switch state {
	case TaskScheduled:
		return asynqQueuePrefix(queue) + "scheduled", true // base.ScheduledKey
	case TaskRetry:
		return asynqQueuePrefix(queue) + "retry", true // base.RetryKey
	case TaskArchived:
		return asynqQueuePrefix(queue) + "archived", true // base.ArchivedKey
	case TaskCompleted:
		return asynqQueuePrefix(queue) + "completed", true // base.CompletedKey
	}
	return "", false
}

// keyReader is the part of the Redis client the direct reads use: redis,
// or a fake in tests.
type keyReader interface {
	// zrange returns the members of a sorted set between ranks start and
	// stop (inclusive) with their scores, lowest score first, or highest
	// first with rev (ZRANGE / ZREVRANGE ... WITHSCORES).
	zrange(ctx context.Context, key string, start, stop int64, rev bool) ([]redis.Z, error)
}

type redisKeys struct{ c redis.UniversalClient }

func (r redisKeys) zrange(ctx context.Context, key string, start, stop int64, rev bool) ([]redis.Z, error) {
	if rev {
		return r.c.ZRevRangeWithScores(ctx, key, start, stop).Result()
	}
	return r.c.ZRangeWithScores(ctx, key, start, stop).Result()
}

// keyEntry is one task ID read from a state's sorted set, with its score
// as a time.
type keyEntry struct {
	ID    string
	Score time.Time
}

// readStateIDs reads one page (offset off, size n) of the IDs of the
// queue's tasks in state with one range read: lowest score first or, with
// desc, highest first. A state without a sorted set returns
// ErrUnsupported.
func readStateIDs(ctx context.Context, r keyReader, queue string, state TaskState, off, n int, desc bool) ([]keyEntry, error) {
	key, ok := stateKey(queue, state)
	if !ok {
		return nil, ErrUnsupported
	}
	zs, err := r.zrange(ctx, key, int64(off), int64(off+n-1), desc)
	if err != nil {
		return nil, err
	}
	out := make([]keyEntry, 0, len(zs))
	for _, z := range zs {
		id, ok := z.Member.(string)
		if !ok {
			continue
		}
		out = append(out, keyEntry{ID: id, Score: time.Unix(int64(z.Score), 0)})
	}
	return out, nil
}
