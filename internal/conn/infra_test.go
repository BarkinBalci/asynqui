package conn

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// errNoInfo is the INFO reply of test connections that have no fake.
var errNoInfo = errors.New("no INFO in this test")

// infoFunc is a fake INFO.
type infoFunc func(ctx context.Context, sections ...string) (string, error)

func (f infoFunc) Info(ctx context.Context, sections ...string) (string, error) {
	return f(ctx, sections...)
}

func TestParseInfoMetrics(t *testing.T) {
	type metrics struct {
		Mode                     string
		Uptime                   time.Duration
		Clients, Used, Peak, Max int64
		Policy                   string
		Keyspace                 Keyspace
	}
	tests := []struct {
		fixture string
		db      int
		want    metrics
	}{
		{"info_redis.txt", 0, metrics{"standalone", 86523 * time.Second, 3, 1258792, 1480432, 0, "noeviction", Keyspace{DB: 0, Keys: 128, Expires: 4}}},
		{"info_redis.txt", 2, metrics{"standalone", 86523 * time.Second, 3, 1258792, 1480432, 0, "noeviction", Keyspace{DB: 2, Keys: 7}}},
		// An empty DB has no Keyspace line.
		{"info_redis.txt", 5, metrics{"standalone", 86523 * time.Second, 3, 1258792, 1480432, 0, "noeviction", Keyspace{DB: 5}}},
		// Valkey writes server_mode instead of redis_mode.
		{"info_valkey.txt", 0, metrics{"standalone", 86523 * time.Second, 3, 1258792, 1480432, 0, "noeviction", Keyspace{DB: 0, Keys: 3, Expires: 1}}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/db%d", tt.fixture, tt.db), func(t *testing.T) {
			i := parseInfo(readInfoFixture(t, tt.fixture), tt.db)
			got := metrics{i.Mode, i.Uptime, i.ConnectedClients, i.UsedMemory, i.UsedMemoryPeak, i.MaxMemory, i.MaxMemoryPolicy, i.Keyspace}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("metrics (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseInfoKeyspaceEdges(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Keyspace
	}{
		{"db1 is not db10", "# Keyspace\r\ndb10:keys=9,expires=9\r\ndb1:keys=1,expires=0\r\n", Keyspace{DB: 1, Keys: 1}},
		{"junk values", "# Keyspace\r\ndb1:keys=x,expires=2,=,novalue\r\n", Keyspace{DB: 1, Expires: 2}},
		{"no keyspace section", "# Server\r\nredis_version:7.0.0\r\n", Keyspace{DB: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseInfo(tt.raw, 1).Keyspace; got != tt.want {
				t.Errorf("Keyspace = %+v, want %+v", got, tt.want)
			}
		})
	}
	if i := parseInfo("# Server\r\nredis_mode:cluster\r\nserver_mode:x\r\nuptime_in_seconds:abc\r\n", 0); i.Mode != "cluster" || i.Uptime != 0 {
		t.Errorf("mode %q uptime %v", i.Mode, i.Uptime)
	}
}

// Backend is detected by Info fetches, and by the health loop each time the
// connection becomes reachable, or while it is still unknown.
func TestBackendDetection(t *testing.T) {
	valkey := "# Server\r\nredis_version:7.2.4\r\nserver_name:valkey\r\nvalkey_version:8.0.2\r\n"
	redis := "# Server\r\nredis_version:7.4.1\r\n"

	t.Run("info", func(t *testing.T) {
		c := testConn(t, "main", nil)
		if _, known := c.Backend(); known {
			t.Fatal("backend known before any INFO")
		}
		c.info = infoFunc(func(_ context.Context, s ...string) (string, error) {
			if len(s) != 0 {
				t.Errorf("Info sections = %q, want the default", s)
			}
			return valkey, nil
		})
		info, err := c.Info(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if info.Backend != BackendValkey || info.Version != "8.0.2" {
			t.Errorf("Info = %v %q", info.Backend, info.Version)
		}
		if b, known := c.Backend(); !known || b != BackendValkey {
			t.Errorf("Backend = %v, %v", b, known)
		}
	})

	t.Run("health loop", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var (
				mu      sync.Mutex
				up      = true
				reply   = valkey
				failing = true // the first detection fails
				calls   []string
			)
			c := testConn(t, "main", pingFunc(func(context.Context) error {
				mu.Lock()
				defer mu.Unlock()
				if !up {
					return errors.New("down")
				}
				return nil
			}))
			c.info = infoFunc(func(_ context.Context, s ...string) (string, error) {
				mu.Lock()
				defer mu.Unlock()
				calls = append(calls, fmt.Sprint(s))
				if failing {
					failing = false
					return "", errors.New("NOPERM")
				}
				return reply, nil
			})
			set := func(u bool, r string) {
				mu.Lock()
				defer mu.Unlock()
				up, reply = u, r
			}
			ncalls := func() int {
				mu.Lock()
				defer mu.Unlock()
				return len(calls)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { c.healthLoop(ctx, 10*time.Second); close(done) }()
			tick := func() { time.Sleep(10 * time.Second); synctest.Wait() }

			synctest.Wait()
			if _, known := c.Backend(); known || ncalls() != 1 {
				t.Fatalf("after a failed detection: known=%v calls=%d", known, ncalls())
			}
			tick() // still unknown: detects again
			if b, known := c.Backend(); !known || b != BackendValkey || ncalls() != 2 {
				t.Fatalf("Backend = %v %v after %d calls", b, known, ncalls())
			}
			tick() // up and known: no INFO
			if ncalls() != 2 {
				t.Errorf("%d INFO calls while up, want 2", ncalls())
			}
			set(false, redis)
			tick() // down: no INFO
			set(true, redis)
			tick() // reachable again: detects the swapped server
			if b, _ := c.Backend(); b != BackendRedis || ncalls() != 3 {
				t.Errorf("Backend = %v after %d calls, want redis after 3", b, ncalls())
			}
			tick() // check 5: up and known
			if ncalls() != 3 {
				t.Errorf("%d INFO calls on check 5, want 3", ncalls())
			}
			set(true, valkey)
			tick() // check 6: the periodic detection finds the swap
			if b, _ := c.Backend(); b != BackendValkey || ncalls() != 4 {
				t.Errorf("Backend = %v after %d calls, want valkey after 4", b, ncalls())
			}
			mu.Lock()
			if calls[0] != "[server]" {
				t.Errorf("detection reads %s, want INFO server", calls[0])
			}
			mu.Unlock()
			cancel()
			<-done
		})
	})
}

func TestTLSAndUsername(t *testing.T) {
	cc := testConnection("main")
	cc.Username = "asynq-ui"
	c, err := newConn(cc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.TLS() != (TLSInfo{}) || c.Username() != "asynq-ui" {
		t.Errorf("TLS %+v username %q", c.TLS(), c.Username())
	}
	cc.TLS.Enabled, cc.TLS.InsecureSkipVerify = true, true
	if c, _ = newConn(cc, nil); c.TLS() != (TLSInfo{Enabled: true}) {
		t.Errorf("insecure TLS = %+v", c.TLS())
	}
	cc.TLS.InsecureSkipVerify = false
	if c, _ = newConn(cc, nil); c.TLS() != (TLSInfo{Enabled: true, Verified: true}) {
		t.Errorf("TLS = %+v", c.TLS())
	}
	// Each handshake records its version through VerifyConnection.
	tc := c.redis.(*redis.Client).Options().TLSConfig
	if err := tc.VerifyConnection(tls.ConnectionState{Version: tls.VersionTLS13}); err != nil {
		t.Fatal(err)
	}
	if got := c.TLS(); got != (TLSInfo{Enabled: true, Verified: true, Version: tls.VersionTLS13}) {
		t.Errorf("after a handshake TLS = %+v", got)
	}
}

func TestUnreachable(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{fmt.Errorf("info: %w", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}), true},
		{fmt.Errorf("x: %w", io.EOF), true},
		{context.DeadlineExceeded, true},
		{redis.ErrPoolTimeout, true},
		{redis.ErrClosed, true},
		{fmt.Errorf("info: %w", replyErr("NOPERM User asynq-ui has no permissions to run the 'info' command")), false},
		{replyErr("LOADING Redis is loading the dataset in memory"), false},
		{errors.New("list servers: cannot parse"), false},
	}
	for _, tt := range tests {
		if got := Unreachable(tt.err); got != tt.want {
			t.Errorf("Unreachable(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestRawForDB(t *testing.T) {
	raw := "# Server\r\nredis_version:8.0.5\r\ndb2_note:kept\r\n\r\n# Keyspace\r\ndb0:keys=1,expires=0\r\ndb2:keys=7,expires=0\r\ndb10:keys=3,expires=0\r\n\r\n# Keysizes\r\ndb0_distrib_strings_sizes:1=2\r\ndb2_distrib_strings_sizes:4=1\r\ndbx:odd\r\n"
	want := "# Server\r\nredis_version:8.0.5\r\ndb2_note:kept\r\n\r\n# Keyspace\r\ndb2:keys=7,expires=0\r\n\r\n# Keysizes\r\ndb2_distrib_strings_sizes:4=1\r\ndbx:odd\r\n"
	i := parseInfo(raw, 2)
	if i.Raw != want {
		t.Errorf("Raw =\n%q\nwant\n%q", i.Raw, want)
	}
	if i.Keyspace != (Keyspace{DB: 2, Keys: 7}) {
		t.Errorf("Keyspace = %+v", i.Keyspace)
	}
}

// fakeServers is a fake Inspector.Servers.
type fakeServers struct {
	infos []*asynq.ServerInfo
	err   error
	calls atomic.Int32
}

func (f *fakeServers) Servers() ([]*asynq.ServerInfo, error) {
	f.calls.Add(1)
	return f.infos, f.err
}

func TestServers(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	fake := &fakeServers{infos: []*asynq.ServerInfo{
		{ID: "b", Host: "server-2", PID: 7, Concurrency: 10, Queues: map[string]int{"low": 1, "critical": 6, "default": 3, "emails": 3},
			StrictPriority: true, Started: t0, Status: "active", ActiveWorkers: []*asynq.WorkerInfo{
				{TaskID: "t2", TaskType: "email:send", Queue: "emails", Started: t0.Add(2 * time.Minute), Deadline: t0.Add(time.Hour)},
				{TaskID: "t1", TaskType: "order:ship", Queue: "critical", Started: t0.Add(time.Minute), TaskPayload: []byte("{}")},
			}},
		{ID: "a", Host: "server-2", PID: 3, Status: "stopped", ActiveWorkers: []*asynq.WorkerInfo{}},
		{ID: "c", Host: "server-1", PID: 9, Status: "active"},
	}}
	c := testConn(t, "main", nil)
	c.servers = fake
	got, err := c.Servers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []Server{
		{ID: "c", Host: "server-1", PID: 9, Status: "active"},
		{ID: "a", Host: "server-2", PID: 3, Status: "stopped"},
		{ID: "b", Host: "server-2", PID: 7, Concurrency: 10, StrictPriority: true, Started: t0, Status: "active",
			Queues: []QueueWeight{{"critical", 6}, {"default", 3}, {"emails", 3}, {"low", 1}},
			Workers: []Worker{
				{TaskID: "t1", TaskType: "order:ship", Queue: "critical", Started: t0.Add(time.Minute)},
				{TaskID: "t2", TaskType: "email:send", Queue: "emails", Started: t0.Add(2 * time.Minute), Deadline: t0.Add(time.Hour)},
			}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Servers (-want +got):\n%s", diff)
	}
	if _, err := c.Servers(t.Context()); err != nil || fake.calls.Load() != 1 {
		t.Errorf("second read: err %v, %d calls, want 1 (cached)", err, fake.calls.Load())
	}

	fail := &fakeServers{err: errors.New("boom")}
	c2 := testConn(t, "main", nil)
	c2.servers = fail
	if _, err := c2.Servers(t.Context()); err == nil || err.Error() != "list servers: boom" {
		t.Errorf("err = %v", err)
	}
}

// fakeSchedulers is a fake of the scheduler part of the Inspector.
type fakeSchedulers struct {
	mu      sync.Mutex
	entries []*asynq.SchedulerEntry
	err     error
	// events by entry ID, newest first, as asynq stores them.
	events map[string][]*asynq.SchedulerEnqueueEvent
	// tasks are the stored task IDs by queue.
	tasks   map[string][]string
	taskErr error
	lists   []string // "size/page" per ListSchedulerEnqueueEvents call
	lookups atomic.Int32
}

func (f *fakeSchedulers) SchedulerEntries() ([]*asynq.SchedulerEntry, error) {
	return f.entries, f.err
}

// pageOpts mirrors asynq's composeListOptions for the two options used.
func pageOpts(opts []asynq.ListOption) (size, page int) {
	size, page = 30, 1
	for _, o := range opts {
		s := fmt.Sprint(o) // pageSizeOpt and pageNumOpt print as their ints
		var n int
		fmt.Sscan(s, &n) //nolint:errcheck // test helper
		switch fmt.Sprintf("%T", o) {
		case "asynq.pageSizeOpt":
			size = n
		case "asynq.pageNumOpt":
			page = n
		}
	}
	return size, page
}

func (f *fakeSchedulers) ListSchedulerEnqueueEvents(id string, opts ...asynq.ListOption) ([]*asynq.SchedulerEnqueueEvent, error) {
	size, page := pageOpts(opts)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists = append(f.lists, fmt.Sprintf("%d/%d", size, page))
	evs := f.events[id]
	start := (page - 1) * size
	if start >= len(evs) {
		return nil, nil
	}
	return evs[start:min(start+size, len(evs))], nil
}

func (f *fakeSchedulers) GetTaskInfo(queue, id string) (*asynq.TaskInfo, error) {
	f.lookups.Add(1)
	if f.taskErr != nil {
		return nil, f.taskErr
	}
	ids, ok := f.tasks[queue]
	if !ok {
		return nil, fmt.Errorf("asynq: %w", asynq.ErrQueueNotFound)
	}
	if slices.Contains(ids, id) {
		return &asynq.TaskInfo{ID: id, Queue: queue}, nil
	}
	return nil, fmt.Errorf("asynq: %w", asynq.ErrTaskNotFound)
}

func TestSchedulerEntries(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 6, 0, 0, 0, time.UTC)
	fake := &fakeSchedulers{entries: []*asynq.SchedulerEntry{
		{ID: "e2", Spec: "@every 30s", Task: asynq.NewTask("webhook:deliver", []byte(`{"a":1}`)),
			Opts: []asynq.Option{asynq.Queue("critical"), asynq.MaxRetry(3)}, Next: t0.Add(30 * time.Second), Prev: t0},
		// The queue on the task is not recorded: unknown.
		{ID: "e1", Spec: "0 6 * * *", Task: asynq.NewTask("report:daily", nil, asynq.Queue("reports")), Next: t0.Add(24 * time.Hour),
			Opts: []asynq.Option{asynq.Timeout(2 * time.Minute), asynq.Unique(time.Hour), asynq.ProcessAt(t0), asynq.Retention(time.Hour)}},
	}}
	c := testConn(t, "main", nil)
	c.schedulers = fake
	got, err := c.SchedulerEntries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []SchedulerEntry{
		{ID: "e1", Spec: "0 6 * * *", TaskType: "report:daily", Next: t0.Add(24 * time.Hour), Options: []EntryOption{
			{"timeout", "2m0s"}, {"unique", "1h0m0s"}, {"process_at", "2026-11-08T06:00:00Z"}, {"retention", "1h0m0s"},
		}},
		{ID: "e2", Spec: "@every 30s", TaskType: "webhook:deliver", Payload: []byte(`{"a":1}`),
			Options: []EntryOption{{"queue", "critical"}, {"max_retry", "3"}}, Queue: "critical", Next: t0.Add(30 * time.Second), Prev: t0},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SchedulerEntries (-want +got):\n%s", diff)
	}

	c2 := testConn(t, "main", nil)
	c2.schedulers = &fakeSchedulers{err: errors.New("boom")}
	if _, err := c2.SchedulerEntries(t.Context()); err == nil || err.Error() != "list scheduler entries: boom" {
		t.Errorf("err = %v", err)
	}
}

func TestSchedulerEnqueueEvents(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 6, 0, 0, 0, time.UTC)
	var evs []*asynq.SchedulerEnqueueEvent
	for i := range 5 { // newest first
		evs = append(evs, &asynq.SchedulerEnqueueEvent{TaskID: fmt.Sprintf("t%d", 5-i), EnqueuedAt: t0.Add(-time.Duration(i) * time.Minute)})
	}
	newFake := func() *fakeSchedulers {
		return &fakeSchedulers{
			events: map[string][]*asynq.SchedulerEnqueueEvent{"e1": evs},
			tasks:  map[string][]string{"reports": {"t5", "t2"}},
		}
	}
	tests := []struct {
		name       string
		queue      string
		page, size int
		want       EnqueuePage
		wantLists  []string
	}{
		{"first page, more follow", "reports", 1, 2, EnqueuePage{Events: []EnqueueEvent{
			{TaskID: "t5", EnqueuedAt: t0, TaskExists: true},
			{TaskID: "t4", EnqueuedAt: t0.Add(-time.Minute)},
		}, HasNext: true}, []string{"2/1", "1/3"}},
		{"middle page", "reports", 2, 2, EnqueuePage{Events: []EnqueueEvent{
			{TaskID: "t3", EnqueuedAt: t0.Add(-2 * time.Minute)},
			{TaskID: "t2", EnqueuedAt: t0.Add(-3 * time.Minute), TaskExists: true},
		}, HasNext: true}, []string{"2/2", "1/5"}},
		{"last, short page: no peek", "reports", 3, 2, EnqueuePage{Events: []EnqueueEvent{
			{TaskID: "t1", EnqueuedAt: t0.Add(-4 * time.Minute)},
		}}, []string{"2/3"}},
		{"full last page: the peek finds nothing", "reports", 1, 5, EnqueuePage{Events: []EnqueueEvent{
			{TaskID: "t5", EnqueuedAt: t0, TaskExists: true},
			{TaskID: "t4", EnqueuedAt: t0.Add(-time.Minute)},
			{TaskID: "t3", EnqueuedAt: t0.Add(-2 * time.Minute)},
			{TaskID: "t2", EnqueuedAt: t0.Add(-3 * time.Minute), TaskExists: true},
			{TaskID: "t1", EnqueuedAt: t0.Add(-4 * time.Minute)},
		}}, []string{"5/1", "1/6"}},
		{"past the end", "reports", 9, 2, EnqueuePage{}, []string{"2/9"}},
		{"queue gone: nothing exists", "gone", 1, 1, EnqueuePage{Events: []EnqueueEvent{
			{TaskID: "t5", EnqueuedAt: t0},
		}, HasNext: true}, []string{"1/1", "1/2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFake()
			c := testConn(t, "main", nil)
			c.schedulers = fake
			got, err := c.SchedulerEnqueueEvents(t.Context(), "e1", tt.queue, tt.page, tt.size)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("page (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantLists, fake.lists); diff != "" {
				t.Errorf("list calls (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("bounds", func(t *testing.T) {
		c := testConn(t, "main", nil)
		c.schedulers = newFake()
		for _, p := range [][2]int{{0, 20}, {1, 0}, {1, MaxEnqueuePageSize + 1}} {
			if _, err := c.SchedulerEnqueueEvents(t.Context(), "e1", "reports", p[0], p[1]); !errors.Is(err, ErrPage) {
				t.Errorf("page %d size %d: err = %v", p[0], p[1], err)
			}
		}
	})

	// asynq keeps 1000 events per entry; the last page offers no older one.
	t.Run("page cap", func(t *testing.T) {
		if MaxEnqueuePage(20) != 50 || MaxEnqueuePage(30) != 34 || MaxEnqueuePage(100) != 10 || MaxEnqueuePage(0) != 0 {
			t.Errorf("MaxEnqueuePage = %d %d %d", MaxEnqueuePage(20), MaxEnqueuePage(30), MaxEnqueuePage(100))
		}
		var many []*asynq.SchedulerEnqueueEvent
		for i := range 1200 { // more than asynq keeps, to check the cap alone stops paging
			many = append(many, &asynq.SchedulerEnqueueEvent{TaskID: fmt.Sprintf("t%d", i)})
		}
		fake := &fakeSchedulers{events: map[string][]*asynq.SchedulerEnqueueEvent{"e1": many}}
		c := testConn(t, "main", nil)
		c.schedulers = fake
		p, err := c.SchedulerEnqueueEvents(t.Context(), "e1", "", 34, 30)
		if err != nil || len(p.Events) != 30 || p.HasNext {
			t.Errorf("last page: %d events, next %v, err %v", len(p.Events), p.HasNext, err)
		}
		if _, err := c.SchedulerEnqueueEvents(t.Context(), "e1", "", 35, 30); !errors.Is(err, ErrPage) {
			t.Errorf("past the cap: err = %v", err)
		}
		if diff := cmp.Diff([]string{"30/34"}, fake.lists); diff != "" {
			t.Errorf("list calls (-want +got):\n%s", diff)
		}
	})

	t.Run("unknown queue: no lookups, nothing linked", func(t *testing.T) {
		fake := newFake()
		c := testConn(t, "main", nil)
		c.schedulers = fake
		p, err := c.SchedulerEnqueueEvents(t.Context(), "e1", "", 1, 5)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range p.Events {
			if e.TaskExists {
				t.Errorf("%s linked", e.TaskID)
			}
		}
		if n := fake.lookups.Load(); n != 0 {
			t.Errorf("%d lookups", n)
		}
	})

	t.Run("task lookup fails: not linked, not an error", func(t *testing.T) {
		fake := newFake()
		fake.taskErr = errors.New("LOADING")
		c := testConn(t, "main", nil)
		c.schedulers = fake
		p, err := c.SchedulerEnqueueEvents(t.Context(), "e1", "reports", 1, 2)
		if err != nil || len(p.Events) != 2 || p.Events[0].TaskExists {
			t.Errorf("page %+v, err %v", p, err)
		}
	})

	// Lookups outlive the page snapshot.
	t.Run("lookups are cached", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := newFake()
			c := testConn(t, "main", nil)
			c.schedulers = fake
			read := func() {
				t.Helper()
				if _, err := c.SchedulerEnqueueEvents(t.Context(), "e1", "reports", 1, 5); err != nil {
					t.Fatal(err)
				}
			}
			read()
			time.Sleep(2 * time.Second) // the page snapshot expired
			read()
			if n := fake.lookups.Load(); n != 5 {
				t.Errorf("%d lookups within 10s, want 5", n)
			}
			time.Sleep(10 * time.Second)
			read()
			if n := fake.lookups.Load(); n != 10 {
				t.Errorf("%d lookups after 10s, want 10", n)
			}
		})
	})

	t.Run("cache keys do not collide", func(t *testing.T) {
		if enqueueKey("a/b", "c", 1, 2) == enqueueKey("a", "b/c", 1, 2) || taskExistsKey("a/b", "c") == taskExistsKey("a", "b/c") {
			t.Error("keys collide")
		}
	})
}

// replyErr is an error reply from Redis, as go-redis returns it.
type replyErr string

func (e replyErr) Error() string { return string(e) }
func (replyErr) RedisError()     {}
