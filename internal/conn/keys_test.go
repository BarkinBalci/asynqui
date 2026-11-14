package conn

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/redis/go-redis/v9"
)

// fakeKeys is a hand-written keyReader over in-memory sorted sets (kept in
// score order) and lists (head first), with Redis's range semantics.
type fakeKeys struct {
	mu    sync.Mutex
	zsets map[string][]redis.Z
	lists map[string][]string
	err   error
	// last records the last read: key, start, stop and rev.
	last struct {
		key         string
		start, stop int64
		rev         bool
	}
	reads int
}

func newFakeKeys() *fakeKeys {
	return &fakeKeys{zsets: map[string][]redis.Z{}, lists: map[string][]string{}}
}

// span resolves Redis's inclusive, possibly negative range over n items.
func span(n int, start, stop int64) (int, int, bool) {
	if start < 0 {
		start += int64(n)
	}
	if stop < 0 {
		stop += int64(n)
	}
	start = max(start, 0)
	stop = min(stop, int64(n)-1)
	if start > stop || start >= int64(n) {
		return 0, 0, false
	}
	return int(start), int(stop) + 1, true
}

func (f *fakeKeys) zrange(_ context.Context, key string, start, stop int64, rev bool) ([]redis.Z, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	f.last.key, f.last.start, f.last.stop, f.last.rev = key, start, stop, rev
	if f.err != nil {
		return nil, f.err
	}
	zs := slices.Clone(f.zsets[key])
	if rev {
		slices.Reverse(zs)
	}
	lo, hi, ok := span(len(zs), start, stop)
	if !ok {
		return nil, nil
	}
	return zs[lo:hi], nil
}

func (f *fakeKeys) lrange(_ context.Context, key string, start, stop int64) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	f.last.key, f.last.start, f.last.stop, f.last.rev = key, start, stop, false
	if f.err != nil {
		return nil, f.err
	}
	l := f.lists[key]
	lo, hi, ok := span(len(l), start, stop)
	if !ok {
		return nil, nil
	}
	return slices.Clone(l[lo:hi]), nil
}

func TestStateKey(t *testing.T) {
	tests := []struct {
		state TaskState
		key   string
		zset  bool
	}{
		{TaskPending, "asynq:{q:1}:pending", false},
		{TaskScheduled, "asynq:{q:1}:scheduled", true},
		{TaskRetry, "asynq:{q:1}:retry", true},
		{TaskArchived, "asynq:{q:1}:archived", true},
		{TaskCompleted, "asynq:{q:1}:completed", true},
	}
	for _, tt := range tests {
		key, zset, ok := stateKey("q:1", tt.state)
		if !ok || key != tt.key || zset != tt.zset {
			t.Errorf("stateKey(%v) = %q, %v, %v; want %q, %v", tt.state, key, zset, ok, tt.key, tt.zset)
		}
	}
	for _, s := range []TaskState{TaskActive, TaskAggregating, 0} {
		if _, _, ok := stateKey("q", s); ok {
			t.Errorf("stateKey(%v) has a key", s)
		}
	}
}

func TestReadStateIDs(t *testing.T) {
	f := newFakeKeys()
	// LPUSH order: p0 first, so the head is the newest.
	f.lists["asynq:{q}:pending"] = []string{"p4", "p3", "p2", "p1", "p0"}
	f.zsets["asynq:{q}:archived"] = []redis.Z{{Score: 100, Member: "x0"}, {Score: 200, Member: "x1"}, {Score: 300, Member: "x2"}}
	ids := func(es []keyEntry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.ID)
		}
		return out
	}
	tests := []struct {
		name   string
		state  TaskState
		off, n int
		desc   bool
		want   []string
	}{
		{"pending oldest first", TaskPending, 0, 2, false, []string{"p0", "p1"}},
		{"pending oldest first, page 2", TaskPending, 2, 2, false, []string{"p2", "p3"}},
		{"pending oldest first, short last page", TaskPending, 4, 2, false, []string{"p4"}},
		{"pending past the end", TaskPending, 6, 2, false, nil},
		{"pending newest first", TaskPending, 0, 2, true, []string{"p4", "p3"}},
		{"pending newest first, last page", TaskPending, 4, 2, true, []string{"p0"}},
		{"archived oldest first", TaskArchived, 1, 5, false, []string{"x1", "x2"}},
		{"archived newest first", TaskArchived, 0, 2, true, []string{"x2", "x1"}},
	}
	for _, tt := range tests {
		got, err := readStateIDs(t.Context(), f, "q", tt.state, tt.off, tt.n, tt.desc)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if diff := cmp.Diff(tt.want, ids(got)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", tt.name, diff)
		}
	}

	got, err := readStateIDs(t.Context(), f, "q", TaskArchived, 0, 1, true)
	if err != nil || len(got) != 1 || !got[0].Score.Equal(time.Unix(300, 0)) {
		t.Errorf("archived score = %+v, %v; want 300", got, err)
	}
	if _, err := readStateIDs(t.Context(), f, "q", TaskActive, 0, 1, false); !errors.Is(err, ErrUnsupported) {
		t.Errorf("active: err = %v, want ErrUnsupported", err)
	}
	f.err = errors.New("LOADING")
	if _, err := readStateIDs(t.Context(), f, "q", TaskPending, 0, 1, false); !errors.Is(err, f.err) {
		t.Errorf("read error: err = %v", err)
	}
}
