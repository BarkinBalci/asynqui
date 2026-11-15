package conn

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/hibiken/asynq"
)

// fakeQueues is a fake queueInspector.
type fakeQueues struct {
	names   []string
	listErr error
	infos   map[string]*asynq.QueueInfo
	errs    map[string]error
	panics  map[string]bool
	// delay is how long each GetQueueInfo takes.
	delay time.Duration
	// history is each queue's DailyStats, newest first.
	history    map[string][]*asynq.DailyStats
	historyErr map[string]error
	// actErr fails PauseQueue, UnpauseQueue and DeleteQueue of a queue.
	actErr map[string]error

	mu       sync.Mutex
	calls    int
	inFlight int
	maxIn    int
	// acted records the actions run, as "pause q", "unpause q", "delete q".
	acted []string
}

func (f *fakeQueues) History(q string, n int) ([]*asynq.DailyStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err := f.historyErr[q]; err != nil {
		return nil, err
	}
	h := f.history[q]
	return h[:min(n, len(h))], nil
}

func (f *fakeQueues) act(verb, q string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acted = append(f.acted, verb+" "+q)
	return f.actErr[q]
}

func (f *fakeQueues) PauseQueue(q string) error   { return f.act("pause", q) }
func (f *fakeQueues) UnpauseQueue(q string) error { return f.act("unpause", q) }
func (f *fakeQueues) DeleteQueue(q string, force bool) error {
	if force {
		return errors.New("force is never used")
	}
	return f.act("delete", q)
}

func (f *fakeQueues) Queues() ([]string, error) {
	return f.names, f.listErr
}

func (f *fakeQueues) GetQueueInfo(q string) (*asynq.QueueInfo, error) {
	f.mu.Lock()
	f.calls++
	f.inFlight++
	f.maxIn = max(f.maxIn, f.inFlight)
	f.mu.Unlock()
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inFlight--
	if f.panics[q] {
		panic("unexpected reply")
	}
	if err := f.errs[q]; err != nil {
		return nil, err
	}
	return f.infos[q], nil
}

func TestSummary(t *testing.T) {
	errBoom := errors.New("boom")
	infos := func(pausedCritical, pausedDefault bool) map[string]*asynq.QueueInfo {
		return map[string]*asynq.QueueInfo{
			"default":  {Queue: "default", Pending: 5, Active: 2, Retry: 1, Scheduled: 9, Archived: 4, Processed: 100, Failed: 3, Latency: 30 * time.Second, Paused: pausedDefault},
			"critical": {Queue: "critical", Pending: 1, Active: 1, Processed: 10, Failed: 1, Latency: 2 * time.Minute, Paused: pausedCritical},
			"low":      {Queue: "low", Retry: 2},
		}
	}
	all := []string{"default", "critical", "low"}
	totals := Summary{Queues: 3, Pending: 6, Active: 3, Retry: 3, ProcessedToday: 110, FailedToday: 4}
	with := func(oldest time.Duration) Summary {
		s := totals
		s.OldestPending = oldest
		return s
	}
	tests := []struct {
		name    string
		fake    *fakeQueues
		want    Summary
		wantErr string
	}{
		{
			name: "no queues",
			fake: &fakeQueues{},
			want: Summary{},
		},
		{
			name: "sums and oldest pending",
			fake: &fakeQueues{names: all, infos: infos(false, false)},
			want: with(2 * time.Minute),
		},
		{
			name: "paused queue counted but not in oldest pending",
			fake: &fakeQueues{names: all, infos: infos(true, false)},
			want: with(30 * time.Second),
		},
		{
			name: "every queue with pending tasks paused",
			fake: &fakeQueues{names: all, infos: infos(true, true)},
			want: with(0),
		},
		{
			// Also what a queue deleted after the listing does: the next
			// poll recovers.
			name:    "queue fails",
			fake:    &fakeQueues{names: all, infos: infos(false, false), errs: map[string]error{"critical": errBoom}},
			wantErr: "queue critical: boom",
		},
		{
			name:    "queue panics",
			fake:    &fakeQueues{names: all, infos: infos(false, false), panics: map[string]bool{"low": true}},
			wantErr: "queue low: conn: panic: unexpected reply",
		},
		{
			name:    "listing fails",
			fake:    &fakeQueues{listErr: errBoom},
			wantErr: "list queues: boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testConn(t, "main", nil)
			c.queues = tt.fake
			got, err := c.Summary(t.Context())
			var gotErr string
			if err != nil {
				gotErr = err.Error()
			}
			if gotErr != tt.wantErr {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Summary (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSummaryConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := &fakeQueues{infos: map[string]*asynq.QueueInfo{}, delay: time.Second}
		for i := range 3*queueConcurrency + 1 {
			q := fmt.Sprintf("q%02d", i)
			fake.names = append(fake.names, q)
			fake.infos[q] = &asynq.QueueInfo{Queue: q, Pending: 1}
		}
		c := testConn(t, "main", nil)
		c.queues = fake

		start := time.Now()
		got, err := c.Summary(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got.Queues != len(fake.names) || got.Pending != len(fake.names) {
			t.Errorf("Summary = %+v, want %d queues and pending", got, len(fake.names))
		}
		if fake.maxIn != queueConcurrency {
			t.Errorf("%d GetQueueInfo calls in flight at most, want %d", fake.maxIn, queueConcurrency)
		}
		if took := time.Since(start); took != 4*time.Second {
			t.Errorf("took %v, want 4 rounds of 1s", took)
		}
	})
}

// A hung Redis makes every GetQueueInfo fail after its socket timeouts. The
// slots stay taken until the calls return, and after the first failure no
// more queues are read, so the Summary fails after one round, not one per
// queueConcurrency queues.
func TestSummaryHungRedis(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := &fakeQueues{errs: map[string]error{}, delay: 12 * time.Second}
		for i := range 80 {
			q := fmt.Sprintf("q%02d", i)
			fake.names = append(fake.names, q)
			fake.errs[q] = errors.New("i/o timeout")
		}
		c := testConn(t, "main", nil)
		c.queues = fake

		start := time.Now()
		_, err := c.Summary(t.Context())
		if err == nil || !strings.HasSuffix(err.Error(), "i/o timeout") {
			t.Fatalf("error = %v, want the i/o timeout", err)
		}
		if took := time.Since(start); took != 12*time.Second {
			t.Errorf("Summary failed after %v, want one round of 12s", took)
		}
		if fake.maxIn != queueConcurrency || fake.calls != queueConcurrency {
			t.Errorf("%d calls, at most %d in flight; want %d and %d", fake.calls, fake.maxIn, queueConcurrency, queueConcurrency)
		}
	})
}
