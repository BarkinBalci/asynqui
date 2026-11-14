package conn

// Direct reads of asynq's own Redis keys.
//
// This file is the one place asynqui assumes asynq's key layout. The
// Inspector cannot list a state in reverse, so the task tables' time sort
// reads one page of IDs straight from the state's key, in the requested
// order, and then hydrates each ID with the public Inspector.GetTaskInfo.
// Only plain, read-only range reads are allowed here: no writes, no Lua,
// no scans, no client-side sorting.
//
// Checked against hibiken/asynq v0.26.0 (the version in go.mod),
// internal/base/base.go and internal/rdb. TestIntegrationKeys pins every
// key and score below against that version: enqueue, schedule, retry,
// archive and complete real tasks, then compare the raw reads. Re-check
// both when bumping asynq.
//
// Keys read, for queue q:
//
//	asynq:{q}:pending    LIST  base.PendingKey. Enqueue LPUSHes to the head;
//	                           workers RPOPLPUSH from the tail, so the tail
//	                           is the oldest (next to run).
//	asynq:{q}:scheduled  ZSET  base.ScheduledKey. Score: process_at (Unix s).
//	asynq:{q}:retry      ZSET  base.RetryKey. Score: next process_at (Unix s).
//	asynq:{q}:archived   ZSET  base.ArchivedKey. Score: when the task was
//	                           archived (Unix s).
//	asynq:{q}:completed  ZSET  base.CompletedKey. Score: completed_at +
//	                           retention, the expiry (Unix s).

import (
	"context"
	"slices"
	"time"

	"github.com/redis/go-redis/v9"
)

// asynqQueuePrefix is base.QueueKeyPrefix (asynq v0.26.0
// internal/base/base.go): every key of a queue starts with it. The braces
// are a Redis Cluster hash tag.
func asynqQueuePrefix(queue string) string { return "asynq:{" + queue + "}:" }

// stateKey is the key holding the IDs of the queue's tasks in state, and
// whether it is a sorted set (else a list). States asynqui does not read
// directly (active, aggregating) have none.
func stateKey(queue string, state TaskState) (key string, zset, ok bool) {
	switch state {
	case TaskPending:
		return asynqQueuePrefix(queue) + "pending", false, true // base.PendingKey
	case TaskScheduled:
		return asynqQueuePrefix(queue) + "scheduled", true, true // base.ScheduledKey
	case TaskRetry:
		return asynqQueuePrefix(queue) + "retry", true, true // base.RetryKey
	case TaskArchived:
		return asynqQueuePrefix(queue) + "archived", true, true // base.ArchivedKey
	case TaskCompleted:
		return asynqQueuePrefix(queue) + "completed", true, true // base.CompletedKey
	}
	return "", false, false
}

// keyReader is the part of the Redis client the direct reads use: redis,
// or a fake in tests.
type keyReader interface {
	// zrange returns the members of a sorted set between ranks start and
	// stop (inclusive) with their scores, lowest score first, or highest
	// first with rev (ZRANGE / ZREVRANGE ... WITHSCORES).
	zrange(ctx context.Context, key string, start, stop int64, rev bool) ([]redis.Z, error)
	// lrange returns a list's elements between indexes start and stop
	// (inclusive, negative from the tail), head first (LRANGE).
	lrange(ctx context.Context, key string, start, stop int64) ([]string, error)
}

type redisKeys struct{ c redis.UniversalClient }

func (r redisKeys) zrange(ctx context.Context, key string, start, stop int64, rev bool) ([]redis.Z, error) {
	if rev {
		return r.c.ZRevRangeWithScores(ctx, key, start, stop).Result()
	}
	return r.c.ZRangeWithScores(ctx, key, start, stop).Result()
}

func (r redisKeys) lrange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	return r.c.LRange(ctx, key, start, stop).Result()
}

// keyEntry is one task ID read from a state's key, with its score as a
// time for a sorted set (zero for the pending list).
type keyEntry struct {
	ID    string
	Score time.Time
}

// readStateIDs reads one page (offset off, size n) of the IDs of the
// queue's tasks in state, in a single command: oldest first (the lowest
// score, or the tail of the pending list) or, with desc, newest first. A
// state without a key returns ErrUnsupported.
func readStateIDs(ctx context.Context, r keyReader, queue string, state TaskState, off, n int, desc bool) ([]keyEntry, error) {
	key, zset, ok := stateKey(queue, state)
	if !ok {
		return nil, ErrUnsupported
	}
	start, stop := int64(off), int64(off+n-1)
	if zset {
		zs, err := r.zrange(ctx, key, start, stop, desc)
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
	if !desc {
		// Oldest first: the same ranks counted from the tail, reversed.
		start, stop = -stop-1, -start-1
	}
	ids, err := r.lrange(ctx, key, start, stop)
	if err != nil {
		return nil, err
	}
	if !desc {
		slices.Reverse(ids)
	}
	out := make([]keyEntry, len(ids))
	for i, id := range ids {
		out[i] = keyEntry{ID: id}
	}
	return out, nil
}
