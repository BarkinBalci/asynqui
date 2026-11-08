package conn

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/hibiken/asynq"
)

// fakeQueues is a fake queueInspector. Each Queues call returns the next
// listing (the last one repeats).
type fakeQueues struct {
	listings [][]string
	listErr  error
	infos    map[string]*asynq.QueueInfo
	errs     map[string]error
	// delay is how long each GetQueueInfo takes.
	delay time.Duration

	mu       sync.Mutex
	lists    int
	inFlight int
	maxIn    int
}

func (f *fakeQueues) Queues() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	l := f.listings[min(f.lists, len(f.listings)-1)]
	f.lists++
	return l, nil
}

func (f *fakeQueues) GetQueueInfo(q string) (*asynq.QueueInfo, error) {
	f.mu.Lock()
	f.inFlight++
	f.maxIn = max(f.maxIn, f.inFlight)
	f.mu.Unlock()
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inFlight--
	if err := f.errs[q]; err != nil {
		return nil, err
	}
	return f.infos[q], nil
}

func TestSummary(t *testing.T) {
	errBoom := errors.New("boom")
	infos := map[string]*asynq.QueueInfo{
		"default":  {Queue: "default", Pending: 5, Active: 2, Retry: 1, Scheduled: 9, Archived: 4, Processed: 100, Failed: 3, Latency: 30 * time.Second},
		"critical": {Queue: "critical", Pending: 1, Active: 1, Processed: 10, Failed: 1, Latency: 2 * time.Minute},
		"low":      {Queue: "low", Retry: 2},
	}
	tests := []struct {
		name    string
		fake    *fakeQueues
		want    Summary
		wantErr error
	}{
		{
			name: "no queues",
			fake: &fakeQueues{listings: [][]string{{}}},
			want: Summary{},
		},
		{
			name: "sums and oldest pending",
			fake: &fakeQueues{listings: [][]string{{"default", "critical", "low"}}, infos: infos},
			want: Summary{Queues: 3, Pending: 6, Active: 3, Retry: 3, ProcessedToday: 110, FailedToday: 4, OldestPending: 2 * time.Minute},
		},
		{
			name: "queue deleted after the listing",
			fake: &fakeQueues{
				listings: [][]string{{"default", "critical"}, {"default"}},
				infos:    infos,
				errs:     map[string]error{"critical": errBoom},
			},
			want: Summary{Queues: 1, Pending: 5, Active: 2, Retry: 1, ProcessedToday: 100, FailedToday: 3, OldestPending: 30 * time.Second},
		},
		{
			name: "queue fails",
			fake: &fakeQueues{
				listings: [][]string{{"default", "critical"}},
				infos:    infos,
				errs:     map[string]error{"critical": errBoom},
			},
			wantErr: errBoom,
		},
		{
			name:    "listing fails",
			fake:    &fakeQueues{listErr: errBoom},
			wantErr: errBoom,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testConn(t, "main", nil)
			c.queues = tt.fake
			got, err := c.Summary(t.Context())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
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
		var names []string
		for i := range 3*summaryConcurrency + 1 {
			q := fmt.Sprintf("q%02d", i)
			names = append(names, q)
			fake.infos[q] = &asynq.QueueInfo{Queue: q, Pending: 1}
		}
		fake.listings = [][]string{names}
		c := testConn(t, "main", nil)
		c.queues = fake

		start := time.Now()
		got, err := c.Summary(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got.Queues != len(names) || got.Pending != len(names) {
			t.Errorf("Summary = %+v, want %d queues and pending", got, len(names))
		}
		if fake.maxIn != summaryConcurrency {
			t.Errorf("%d GetQueueInfo calls in flight at most, want %d", fake.maxIn, summaryConcurrency)
		}
		if took := time.Since(start); took != 4*time.Second {
			t.Errorf("took %v, want 4 rounds of 1s", took)
		}
	})
}

func TestSummaryCallTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testConn(t, "main", nil)
		c.timeout = 2 * time.Second
		c.queues = &fakeQueues{
			listings: [][]string{{"slow"}},
			infos:    map[string]*asynq.QueueInfo{"slow": {}},
			delay:    time.Minute, // longer than the timeout
		}
		start := time.Now()
		_, err := c.Summary(t.Context())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context.DeadlineExceeded", err)
		}
		if took := time.Since(start); took != 2*time.Second {
			t.Errorf("Summary returned after %v, want the 2s call timeout", took)
		}
		time.Sleep(time.Minute) // let the abandoned call finish
	})
}
