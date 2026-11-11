package conn

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/hibiken/asynq"
)

func TestQueues(t *testing.T) {
	fake := &fakeQueues{
		names: []string{"low", "critical", "default"},
		infos: map[string]*asynq.QueueInfo{
			"default": {Queue: "default", Size: 20, Pending: 5, Active: 2, Scheduled: 3, Retry: 1, Archived: 4, Aggregating: 5,
				Completed: 9, Processed: 100, Failed: 3, Latency: 30 * time.Second, MemoryUsage: 4096, ProcessedTotal: 999},
			"critical": {Queue: "critical", Paused: true, Size: 1, Pending: 1},
			"low":      {Queue: "low"},
		},
	}
	c := testConn(t, "main", nil)
	c.queues = fake
	got, err := c.Queues(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []Queue{
		{Name: "critical", Paused: true, Size: 1, Pending: 1},
		{Name: "default", Size: 20, Pending: 5, Active: 2, Scheduled: 3, Retry: 1, Archived: 4,
			ProcessedToday: 100, FailedToday: 3, Latency: 30 * time.Second, MemoryUsage: 4096},
		{Name: "low"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Queues (-want +got):\n%s", diff)
	}
}

// Summary and Queues share one fetch: the Overview and the Connection page
// cost one GetQueueInfo per queue per second together.
func TestSummarySharesQueuesFetch(t *testing.T) {
	fake := &fakeQueues{names: []string{"a", "b"}, infos: map[string]*asynq.QueueInfo{
		"a": {Queue: "a", Pending: 1}, "b": {Queue: "b", Pending: 2},
	}}
	c := testConn(t, "main", nil)
	c.queues = fake
	if _, err := c.Queues(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := c.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s.Pending != 3 || s.Queues != 2 {
		t.Errorf("Summary = %+v", s)
	}
	if fake.calls != 2 {
		t.Errorf("%d GetQueueInfo calls, want 2 (one fetch)", fake.calls)
	}
}

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

// stats builds asynq's History reply, newest first, from processed/failed
// pairs given oldest first, ending on 2026-11-08 at 15:04 UTC.
func stats(q string, pf ...int) []*asynq.DailyStats {
	end := time.Date(2026, 11, 8, 15, 4, 0, 0, time.UTC)
	n := len(pf) / 2
	out := make([]*asynq.DailyStats, n)
	for i := range n {
		out[n-1-i] = &asynq.DailyStats{Queue: q, Processed: pf[2*i], Failed: pf[2*i+1], Date: end.AddDate(0, 0, i-n+1)}
	}
	return out
}

// shifted moves every date of a History reply by d.
func shifted(in []*asynq.DailyStats, d time.Duration) []*asynq.DailyStats {
	for _, s := range in {
		s.Date = s.Date.Add(d)
	}
	return in
}

func TestHistory(t *testing.T) {
	errBoom := errors.New("boom")
	history := map[string][]*asynq.DailyStats{
		"a": stats("a", 10, 1, 20, 2, 30, 3),
		"b": stats("b", 1, 0, 2, 0, 3, 1),
	}
	three := func(p0, f0, p1, f1, p2, f2 int) []DailyStat {
		return []DailyStat{
			{Date: day("2026-11-06"), Processed: p0, Failed: f0},
			{Date: day("2026-11-07"), Processed: p1, Failed: f1},
			{Date: day("2026-11-08"), Processed: p2, Failed: f2},
		}
	}
	tests := []struct {
		name    string
		fake    *fakeQueues
		queue   string
		days    int
		want    []DailyStat
		wantErr error
	}{
		{"one queue, oldest first", &fakeQueues{names: []string{"a", "b"}, history: history}, "a", 3, three(10, 1, 20, 2, 30, 3), nil},
		{"all queues summed by day", &fakeQueues{names: []string{"a", "b"}, history: history}, "", 3, three(11, 1, 22, 2, 33, 4), nil},
		{"no queues: zeros ending today", &fakeQueues{}, "", 3, three(0, 0, 0, 0, 0, 0), nil},
		{"single day", &fakeQueues{names: []string{"a"}, history: history}, "a", 1, []DailyStat{{Date: day("2026-11-08"), Processed: 30, Failed: 3}}, nil},
		{"unknown queue", &fakeQueues{names: []string{"a"}, history: history}, "nope", 3, nil, ErrQueueNotFound},
		{"too few days", &fakeQueues{}, "", 0, nil, ErrHistoryRange},
		{"too many days", &fakeQueues{}, "", MaxHistoryDays + 1, nil, ErrHistoryRange},
		{"a queue fails", &fakeQueues{names: []string{"a", "b"}, history: history, historyErr: map[string]error{"b": errBoom}}, "", 3, nil, errBoom},
		{"listing fails", &fakeQueues{listErr: errBoom}, "", 3, nil, errBoom},
		// Summed by UTC day, not by position: a reply dated a day ahead
		// (asynq's clock past midnight) loses its newest day and shifts
		// the rest onto their own dates.
		{"keyed by day", &fakeQueues{names: []string{"a", "b"}, history: map[string][]*asynq.DailyStats{
			"a": stats("a", 10, 1, 20, 2, 30, 3),
			"b": shifted(stats("b", 1, 0, 2, 0, 3, 1), 24*time.Hour),
		}}, "", 3, three(10, 1, 21, 2, 32, 3), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testConn(t, "main", nil)
			c.queues = tt.fake
			c.now = func() time.Time { return time.Date(2026, 11, 8, 23, 0, 0, 0, time.UTC) }
			got, err := c.History(t.Context(), tt.queue, tt.days)
			if !errors.Is(err, tt.wantErr) || (err == nil) != (tt.wantErr == nil) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("History (-want +got):\n%s", diff)
			}
		})
	}
}

func TestHistoryCached(t *testing.T) {
	fake := &fakeQueues{names: []string{"a"}, history: map[string][]*asynq.DailyStats{"a": stats("a", 1, 0, 2, 0)}}
	c := testConn(t, "main", nil)
	c.queues = fake
	for range 3 {
		if _, err := c.History(t.Context(), "a", 2); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.History(t.Context(), "", 2); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 2 {
		t.Errorf("%d History calls, want one per key", fake.calls)
	}
}

func TestQueueActions(t *testing.T) {
	errBoom := errors.New("boom")
	notEmpty := fmt.Errorf("%w: queue=%q", asynq.ErrQueueNotEmpty, "full")
	notFound := fmt.Errorf("%w: queue=%q", asynq.ErrQueueNotFound, "gone")
	fake := func() *fakeQueues {
		return &fakeQueues{names: []string{"ok", "full", "bad"}, actErr: map[string]error{"full": notEmpty, "gone": notFound, "bad": errBoom}}
	}
	type action func(*Conn, context.Context, string) error
	pause, unpause, del := (*Conn).PauseQueue, (*Conn).UnpauseQueue, (*Conn).DeleteQueue
	tests := []struct {
		name      string
		act       action
		queue     string
		wantErr   []error
		wantActed string
	}{
		{"pause", pause, "ok", nil, "pause ok"},
		{"unpause", unpause, "ok", nil, "unpause ok"},
		{"pause unknown queue is never sent", pause, "gone", []error{ErrQueueNotFound}, ""},
		{"unpause blank name", unpause, " ", []error{ErrQueueNotFound}, ""},
		{"pause fails", pause, "bad", []error{errBoom}, "pause bad"},
		{"delete", del, "ok", nil, "delete ok"},
		{"delete not empty", del, "full", []error{ErrQueueNotEmpty, asynq.ErrQueueNotEmpty}, "delete full"},
		{"delete not found", del, "gone", []error{ErrQueueNotFound, asynq.ErrQueueNotFound}, "delete gone"},
		{"delete fails", del, "bad", []error{errBoom}, "delete bad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := fake()
			c := testConn(t, "main", nil)
			c.queues = f
			err := tt.act(c, t.Context(), tt.queue)
			if (err == nil) != (len(tt.wantErr) == 0) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			for _, want := range tt.wantErr {
				if !errors.Is(err, want) {
					t.Errorf("error %v does not wrap %v", err, want)
				}
			}
			var acted string
			if len(f.acted) > 0 {
				acted = f.acted[0]
			}
			if acted != tt.wantActed || len(f.acted) > 1 {
				t.Errorf("acted = %q, want %q", f.acted, tt.wantActed)
			}
		})
	}

	t.Run("canceled context does nothing", func(t *testing.T) {
		f := fake()
		c := testConn(t, "main", nil)
		c.queues = f
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		for _, act := range []action{pause, unpause, del} {
			if err := act(c, ctx, "ok"); !errors.Is(err, context.Canceled) {
				t.Errorf("error = %v", err)
			}
		}
		if len(f.acted) != 0 {
			t.Errorf("acted = %q", f.acted)
		}
	})
}

// A successful action drops the cached reads, so the next poll shows its
// effect; a failed one keeps them.
func TestActionInvalidatesCache(t *testing.T) {
	f := &fakeQueues{
		names:   []string{"a"},
		infos:   map[string]*asynq.QueueInfo{"a": {Queue: "a"}},
		history: map[string][]*asynq.DailyStats{"a": stats("a", 1, 0)},
		actErr:  map[string]error{},
	}
	c := testConn(t, "main", nil)
	c.queues = f
	read := func() {
		t.Helper()
		if _, err := c.Queues(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := c.History(t.Context(), "", 1); err != nil {
			t.Fatal(err)
		}
	}
	calls := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.calls
	}
	read()
	read()
	if n := calls(); n != 2 {
		t.Fatalf("%d calls after two reads, want 2 (cached)", n)
	}

	f.actErr["a"] = errors.New("boom")
	if err := c.PauseQueue(t.Context(), "a"); err == nil {
		t.Fatal("pause succeeded")
	}
	read()
	if n := calls(); n != 2 {
		t.Errorf("%d calls after a failed action, want the cache kept", n)
	}

	for i, act := range []func(context.Context, string) error{c.PauseQueue, c.UnpauseQueue, c.DeleteQueue} {
		delete(f.actErr, "a")
		if err := act(t.Context(), "a"); err != nil {
			t.Fatal(err)
		}
		read()
		if n, want := calls(), 2+2*(i+1); n != want {
			t.Errorf("action %d: %d calls, want %d (refetched)", i, n, want)
		}
	}
}

// A fetch that was in flight when the cache was reset still answers its
// waiters but is not stored: the value may predate the action.
func TestCacheResetDuringFetch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCache()
		const key cacheKey[int32] = "k"
		var fetches atomic.Int32
		release := make(chan struct{})
		slow := func(context.Context) (int32, error) { //nolint:unparam // the signature cached takes
			n := fetches.Add(1)
			if n == 1 {
				<-release
			}
			return n, nil
		}
		first := make(chan int32)
		go func() {
			v, _ := cached(t.Context(), c, key, time.Second, slow)
			first <- v
		}()
		synctest.Wait()
		c.reset()
		// A caller after the reset does not join the old flight.
		if v, _ := cached(t.Context(), c, key, time.Second, slow); v != 2 {
			t.Errorf("after reset got %d, want a fresh fetch", v)
		}
		close(release)
		if v := <-first; v != 1 {
			t.Errorf("waiter got %d, want its own fetch", v)
		}
		if v, _ := cached(t.Context(), c, key, time.Second, slow); v != 2 {
			t.Errorf("stored %d, want the post-reset value", v)
		}
	})
}
