package conn_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/BarkinBalci/asynqui/internal/conn"
)

// TestIntegrationKeys pins the asynq key layout that conn's direct reads
// (keys.go) assume, against the asynq version in go.mod: real tasks are
// enqueued, scheduled, failed into retry, archived and completed, then the
// raw keys are read and their order and scores checked, and so are the
// task tables built on them. A change in asynq's layout fails here.
func TestIntegrationKeys(t *testing.T) {
	_, addr := runRedis(t, redisImage)
	cc := connection("main", addr, 0)
	reg := newRegistry(t, cc)
	c := mustGet(t, reg, "main")
	client := newClient(t, cc)
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := t.Context()

	enq := func(id string, opts ...asynq.Option) {
		t.Helper()
		if _, err := client.Enqueue(asynq.NewTask("t", []byte(id)), append([]asynq.Option{asynq.TaskID(id)}, opts...)...); err != nil {
			t.Fatal(err)
		}
	}
	type entry struct {
		ID    string
		Score int64
	}
	zrange := func(key string) []entry {
		t.Helper()
		zs, err := rdb.ZRangeWithScores(ctx, key, 0, -1).Result()
		if err != nil {
			t.Fatal(err)
		}
		var out []entry
		for _, z := range zs {
			out = append(out, entry{z.Member.(string), int64(z.Score)})
		}
		return out
	}
	list := func(state conn.TaskState, desc bool) []string {
		t.Helper()
		ts, err := c.ListTasks(ctx, "q", state, 1, 20, desc)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, task := range ts {
			out = append(out, task.ID)
		}
		return out
	}

	// Pending: LPUSHed, so the head is the newest.
	for _, id := range []string{"p1", "p2", "p3"} {
		enq(id, asynq.Queue("q"))
	}
	if got, err := rdb.LRange(ctx, "asynq:{q}:pending", 0, -1).Result(); err != nil || !cmp.Equal(got, []string{"p3", "p2", "p1"}) {
		t.Errorf("pending list = %v, %v; want newest at the head", got, err)
	}
	if got := list(conn.TaskPending, false); !cmp.Equal(got, []string{"p1", "p2", "p3"}) {
		t.Errorf("pending oldest first = %v", got)
	}
	if got := list(conn.TaskPending, true); !cmp.Equal(got, []string{"p3", "p2", "p1"}) {
		t.Errorf("pending newest first = %v", got)
	}

	// Scheduled: scored by process_at, whatever the enqueue order.
	base := time.Now().Truncate(time.Second).Add(time.Hour)
	for id, in := range map[string]time.Duration{"s1": 2 * time.Hour, "s2": 3 * time.Hour, "s3": time.Hour} {
		enq(id, asynq.Queue("q"), asynq.ProcessAt(base.Add(in)))
	}
	want := []entry{{"s3", base.Add(time.Hour).Unix()}, {"s1", base.Add(2 * time.Hour).Unix()}, {"s2", base.Add(3 * time.Hour).Unix()}}
	if diff := cmp.Diff(want, zrange("asynq:{q}:scheduled")); diff != "" {
		t.Errorf("scheduled set (-want +got):\n%s", diff)
	}
	if got := list(conn.TaskScheduled, true); !cmp.Equal(got, []string{"s2", "s1", "s3"}) {
		t.Errorf("scheduled latest first = %v", got)
	}

	// Archived: scored by when it was archived. Archive in distinct
	// seconds, in an order that differs from the IDs', so the order can
	// only come from the scores.
	for _, id := range []string{"a1", "a2", "a3"} {
		enq(id, asynq.Queue("q"), asynq.ProcessIn(48*time.Hour))
	}
	var archived []entry
	for _, id := range []string{"a3", "a1", "a2"} {
		before := time.Now().Unix()
		for time.Now().Unix() == before {
			time.Sleep(20 * time.Millisecond)
		}
		if err := c.ArchiveTask(ctx, "q", id); err != nil {
			t.Fatal(err)
		}
		archived = append(archived, entry{id, time.Now().Unix()})
	}
	got := zrange("asynq:{q}:archived")
	if len(got) != 3 {
		t.Fatalf("archived set = %v", got)
	}
	for i, e := range got {
		if e.ID != archived[i].ID || e.Score < archived[i].Score-1 || e.Score > archived[i].Score {
			t.Errorf("archived[%d] = %+v, want %s archived at %d", i, e, archived[i].ID, archived[i].Score)
		}
	}
	ts, err := c.ListTasks(ctx, "q", conn.TaskArchived, 1, 20, true)
	if err != nil || len(ts) != 3 || ts[0].ID != "a2" || ts[2].ID != "a3" {
		t.Fatalf("archived newest first = %+v, %v", ts, err)
	}
	for i, task := range ts {
		if task.ArchivedAt.Unix() != got[2-i].Score {
			t.Errorf("%s ArchivedAt = %v, want its score %d", task.ID, task.ArchivedAt, got[2-i].Score)
		}
	}

	// Retry and completed, through a worker: retry is scored by the next
	// process time, completed by completed_at + retention.
	mux := asynq.NewServeMux()
	mux.HandleFunc("t", func(_ context.Context, task *asynq.Task) error {
		if string(task.Payload())[0] == 'r' {
			return errors.New("boom")
		}
		return nil
	})
	srv := asynq.NewServer(asynq.RedisClientOpt{Addr: addr}, asynq.Config{
		Concurrency: 2,
		Queues:      map[string]int{"work": 1},
		RetryDelayFunc: func(_ int, _ error, task *asynq.Task) time.Duration {
			h, _ := strconv.Atoi(string(task.Payload())[1:])
			return time.Duration(h) * time.Hour
		},
		LogLevel:        asynq.FatalLevel,
		ShutdownTimeout: time.Second,
	})
	if err := srv.Start(mux); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	enq("r2", asynq.Queue("work"), asynq.MaxRetry(3))
	enq("r1", asynq.Queue("work"), asynq.MaxRetry(3))
	enq("c3", asynq.Queue("work"), asynq.Retention(3*time.Hour))
	enq("c1", asynq.Queue("work"), asynq.Retention(time.Hour))
	enq("c2", asynq.Queue("work"), asynq.Retention(2*time.Hour))
	insp := c.Inspector()
	eventually(t, "two retry and three completed tasks", func() bool {
		qi, err := insp.GetQueueInfo("work")
		return err == nil && qi.Retry == 2 && qi.Completed == 3
	})
	srv.Shutdown()

	score := func(id string, at time.Time) entry {
		return entry{id, at.Unix()}
	}
	info := func(id string) *asynq.TaskInfo {
		t.Helper()
		ti, err := insp.GetTaskInfo("work", id)
		if err != nil {
			t.Fatal(err)
		}
		return ti
	}
	want = []entry{score("r1", info("r1").NextProcessAt), score("r2", info("r2").NextProcessAt)}
	if diff := cmp.Diff(want, zrange("asynq:{work}:retry")); diff != "" {
		t.Errorf("retry set (-want +got):\n%s", diff)
	}
	if d := info("r2").NextProcessAt.Sub(info("r1").NextProcessAt); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("retry times %v apart, want 1h", d)
	}
	keptUntil := func(id string) time.Time { ti := info(id); return ti.CompletedAt.Add(ti.Retention) }
	want = []entry{score("c1", keptUntil("c1")), score("c2", keptUntil("c2")), score("c3", keptUntil("c3"))}
	if diff := cmp.Diff(want, zrange("asynq:{work}:completed")); diff != "" {
		t.Errorf("completed set (-want +got):\n%s", diff)
	}
	ts, err = c.ListTasks(ctx, "work", conn.TaskCompleted, 1, 2, true)
	if err != nil || len(ts) != 2 || ts[0].ID != "c3" || ts[1].ID != "c2" {
		t.Errorf("completed latest expiry first = %+v, %v", ts, err)
	}
	ts, err = c.ListTasks(ctx, "work", conn.TaskRetry, 1, 20, false)
	if err != nil || len(ts) != 2 || ts[0].ID != "r1" || ts[1].ID != "r2" {
		t.Errorf("retry soonest first = %+v, %v", ts, err)
	}

	// A queue holding only completed tasks is not empty: asynq's own check
	// would delete it and orphan them.
	if _, err := c.DeleteAllTasks(ctx, "work", conn.TaskRetry); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteQueue(ctx, "work"); !errors.Is(err, conn.ErrQueueNotEmpty) {
		t.Errorf("delete a queue of completed tasks: %v, want ErrQueueNotEmpty", err)
	}
}
