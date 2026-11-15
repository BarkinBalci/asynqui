package conn_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/hibiken/asynq"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/BarkinBalci/asynqui/internal/config"
	"github.com/BarkinBalci/asynqui/internal/conn"
)

// These tests run a real Redis (or Valkey) with testcontainers. They skip
// without Docker unless ASYNQUI_REQUIRE_DOCKER=1, which makes them fail
// instead.

const redisImage = "redis:7.4-alpine"

// runRedis starts a container and returns it with its host:port.
func runRedis(t *testing.T, image string, opts ...testcontainers.ContainerCustomizer) (*tcredis.RedisContainer, string) {
	t.Helper()
	requireDocker(t)
	ctr, err := tcredis.Run(t.Context(), image, opts...)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := ctr.PortEndpoint(t.Context(), "6379/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	return ctr, addr
}

// requireDocker skips the test without a healthy Docker provider, or
// fails it when ASYNQUI_REQUIRE_DOCKER=1.
func requireDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("ASYNQUI_REQUIRE_DOCKER") != "1" {
		testcontainers.SkipIfProviderIsNotHealthy(t)
		return
	}
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		p, err := testcontainers.ProviderDefault.GetProvider()
		if err != nil {
			return err
		}
		defer func() { _ = p.Close() }()
		return p.Health(t.Context())
	}()
	if err != nil {
		t.Fatalf("ASYNQUI_REQUIRE_DOCKER=1 but Docker is unavailable: %v", err)
	}
}

func connection(name, addr string, db int) config.Connection {
	return config.Connection{
		Name:         name,
		Addr:         addr,
		DB:           db,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
	}
}

func newRegistry(t *testing.T, conns ...config.Connection) *conn.Registry {
	t.Helper()
	reg, err := conn.NewRegistry(conns, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reg.Close(); err != nil {
			t.Error(err)
		}
	})
	return reg
}

func mustGet(t *testing.T, reg *conn.Registry, name string) *conn.Conn {
	t.Helper()
	c, ok := reg.Get(name)
	if !ok {
		t.Fatalf("no connection %q", name)
	}
	return c
}

// newClient returns an asynq Client for enqueuing test tasks.
func newClient(t *testing.T, cc config.Connection) *asynq.Client {
	t.Helper()
	c := asynq.NewClient(asynq.RedisClientOpt{Addr: cc.Addr, DB: cc.DB, Username: cc.Username, Password: cc.Password.Reveal()})
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c
}

func enqueue(t *testing.T, c *asynq.Client, n int, taskType string, opts ...asynq.Option) {
	t.Helper()
	for range n {
		if _, err := c.Enqueue(asynq.NewTask(taskType, nil), opts...); err != nil {
			t.Fatal(err)
		}
	}
}

// eventually polls cond every 100ms until it holds or 15s pass.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestIntegrationSummary(t *testing.T) {
	_, addr := runRedis(t, redisImage)
	mainConn := connection("main", addr, 0)
	reg := newRegistry(t, mainConn, connection("other", addr, 1))
	mc := mustGet(t, reg, "main")

	// Pending tasks in two queues, and a scheduled one that is not pending.
	client := newClient(t, mainConn)
	enqueue(t, client, 3, "email", asynq.Queue("default"))
	enqueue(t, client, 2, "email", asynq.Queue("critical"))
	enqueue(t, client, 1, "report", asynq.Queue("low"), asynq.ProcessIn(time.Hour))

	// A worker fails one task into retry and holds another active.
	release := make(chan struct{})
	mux := asynq.NewServeMux()
	mux.HandleFunc("fail", func(context.Context, *asynq.Task) error { return errors.New("boom") })
	mux.HandleFunc("block", func(ctx context.Context, _ *asynq.Task) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	srv := asynq.NewServer(asynq.RedisClientOpt{Addr: addr}, asynq.Config{
		Concurrency:     2,
		Queues:          map[string]int{"flaky": 1, "busy": 1},
		RetryDelayFunc:  func(int, error, *asynq.Task) time.Duration { return time.Hour },
		LogLevel:        asynq.FatalLevel,
		ShutdownTimeout: time.Second,
	})
	if err := srv.Start(mux); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(release)
		srv.Shutdown()
	})
	enqueue(t, client, 1, "fail", asynq.Queue("flaky"), asynq.MaxRetry(5))
	enqueue(t, client, 1, "block", asynq.Queue("busy"))
	insp := mc.Inspector()
	eventually(t, "a retry task and an active task", func() bool {
		flaky, err1 := insp.GetQueueInfo("flaky")
		busy, err2 := insp.GetQueueInfo("busy")
		return err1 == nil && err2 == nil && flaky.Retry == 1 && busy.Active == 1
	})

	got, err := mc.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := conn.Summary{Queues: 5, Pending: 5, Active: 1, Retry: 1, ProcessedToday: 1, FailedToday: 1}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(conn.Summary{}, "OldestPending")); diff != "" {
		t.Errorf("Summary (-want +got):\n%s", diff)
	}
	if got.OldestPending <= 0 || got.OldestPending > time.Minute {
		t.Errorf("OldestPending = %v, want the age of the first pending task", got.OldestPending)
	}

	// Another DB of the same instance is separate.
	other, err := mustGet(t, reg, "other").Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if other != (conn.Summary{}) {
		t.Errorf("db 1 Summary = %+v, want empty", other)
	}
}

func TestIntegrationInfo(t *testing.T) {
	tests := []struct {
		image       string
		backend     conn.Backend
		version     string // prefix
		redisPrefix string
		// Valkey writes server_mode, not redis_mode, by default.
		modeKey string
	}{
		{redisImage, conn.BackendRedis, "7.4.", "7.4.", "redis_mode"},
		{"valkey/valkey:8.0-alpine", conn.BackendValkey, "8.0.", "7.2.", "server_mode"},
	}
	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			_, addr := runRedis(t, tt.image)
			reg := newRegistry(t, connection("main", addr, 0))
			info, err := mustGet(t, reg, "main").Info(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if info.Backend != tt.backend || !strings.HasPrefix(info.Version, tt.version) || !strings.HasPrefix(info.RedisVersion, tt.redisPrefix) {
				t.Errorf("backend %v version %q redis_version %q, want %v %s* %s*", info.Backend, info.Version, info.RedisVersion, tt.backend, tt.version, tt.redisPrefix)
			}
			if info.Mode != "standalone" || info.UsedMemory == 0 || info.ConnectedClients == 0 {
				t.Errorf("mode %q (from %s), used memory %d, clients %d", info.Mode, tt.modeKey, info.UsedMemory, info.ConnectedClients)
			}
			if !strings.HasPrefix(info.Raw, "# Server\r\n") {
				t.Errorf("Raw does not start with the server section: %.40q", info.Raw)
			}
		})
	}
}

func TestIntegrationHealth(t *testing.T) {
	ctr, addr := runRedis(t, redisImage)
	reg := newRegistry(t, connection("main", addr, 0))
	c := mustGet(t, reg, "main")
	if got := c.Health().State; got != conn.StateUnknown {
		t.Fatalf("state before Start = %v, want unknown", got)
	}

	reg.Start(t.Context())
	eventually(t, "the first health check", func() bool { return c.Health().State != conn.StateUnknown })
	if h := c.Health(); h.State != conn.StateUp || h.Err != nil || h.Latency <= 0 {
		t.Fatalf("health = %+v, want up", h)
	}

	if err := ctr.Stop(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	h, err := reg.Recheck(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if h.State != conn.StateDown || h.Err == nil {
		t.Fatalf("Recheck after stop = %+v, want down with an error", h)
	}
	if got := c.Health(); got.State != conn.StateDown {
		t.Errorf("stored health = %+v, want down", got)
	}
	if _, err := c.Summary(t.Context()); err == nil {
		t.Error("Summary succeeded with Redis stopped")
	}
}

func TestIntegrationACL(t *testing.T) {
	_, addr := runRedis(t, redisImage, testcontainers.WithCmdArgs(
		"--user", "default", "off",
		"--user", "alice", "on", ">s3cret", "~*", "&*", "+@all",
	))
	withUser := func(name, user, password string) config.Connection {
		cc := connection(name, addr, 0)
		cc.Username, cc.Password = user, config.Secret(password)
		return cc
	}
	reg := newRegistry(t,
		withUser("alice", "alice", "s3cret"),
		withUser("wrong", "alice", "nope"),
		withUser("anonymous", "", ""),
	)

	tests := []struct {
		name    string
		want    conn.State
		errText string
	}{
		{"alice", conn.StateUp, ""},
		{"wrong", conn.StateDown, "WRONGPASS"},
		{"anonymous", conn.StateDown, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := reg.Recheck(t.Context(), tt.name)
			if err != nil {
				t.Fatal(err)
			}
			if h.State != tt.want {
				t.Fatalf("state = %v (%v), want %v", h.State, h.Err, tt.want)
			}
			if tt.errText != "" && (h.Err == nil || !strings.Contains(h.Err.Error(), tt.errText)) {
				t.Errorf("error = %v, want it to contain %q", h.Err, tt.errText)
			}
		})
	}

	// The Inspector authenticates too.
	alice := mustGet(t, reg, "alice")
	enqueue(t, newClient(t, withUser("alice", "alice", "s3cret")), 2, "email")
	s, err := alice.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s.Queues != 1 || s.Pending != 2 {
		t.Errorf("Summary = %+v, want 1 queue with 2 pending", s)
	}
}

func TestIntegrationQueues(t *testing.T) {
	_, addr := runRedis(t, redisImage)
	cc := connection("main", addr, 0)
	reg := newRegistry(t, cc)
	c := mustGet(t, reg, "main")
	client := newClient(t, cc)

	enqueue(t, client, 3, "email", asynq.Queue("default"))
	enqueue(t, client, 1, "report", asynq.Queue("low"), asynq.ProcessIn(time.Hour))

	// A worker processes one task of "work" and fails one of "flaky".
	mux := asynq.NewServeMux()
	mux.HandleFunc("ok", func(context.Context, *asynq.Task) error { return nil })
	mux.HandleFunc("fail", func(context.Context, *asynq.Task) error { return errors.New("boom") })
	srv := asynq.NewServer(asynq.RedisClientOpt{Addr: addr}, asynq.Config{
		Concurrency:     2,
		Queues:          map[string]int{"work": 1, "flaky": 1},
		RetryDelayFunc:  func(int, error, *asynq.Task) time.Duration { return time.Hour },
		LogLevel:        asynq.FatalLevel,
		ShutdownTimeout: time.Second,
	})
	if err := srv.Start(mux); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	// Kept as completed for an hour: asynq counts it in Size.
	enqueue(t, client, 1, "ok", asynq.Queue("work"), asynq.Retention(time.Hour))
	enqueue(t, client, 1, "fail", asynq.Queue("flaky"), asynq.MaxRetry(3))
	eventually(t, "a processed and a failed task", func() bool {
		qs, err := c.Queues(t.Context())
		if err != nil || len(qs) != 4 {
			return false
		}
		by := map[string]conn.Queue{}
		for _, q := range qs {
			by[q.Name] = q
		}
		return by["work"].ProcessedToday == 1 && by["flaky"].FailedToday == 1 && by["flaky"].Retry == 1
	})

	qs, err := c.Queues(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, q := range qs {
		names = append(names, q.Name)
	}
	if diff := cmp.Diff([]string{"default", "flaky", "low", "work"}, names); diff != "" {
		t.Errorf("queue names (-want +got):\n%s", diff)
	}
	def := qs[0]
	if def.Pending != 3 || def.Size != 3 || def.Paused || def.MemoryUsage <= 0 || def.Latency <= 0 {
		t.Errorf("default = %+v", def)
	}
	if low := qs[2]; low.Scheduled != 1 || low.Size != 1 {
		t.Errorf("low = %+v", low)
	}
	// asynq v0.26.0 counts completed tasks in Size (its doc says
	// otherwise); the table shows asynq's number.
	if work := qs[3]; work.Size != 1 || work.Pending != 0 || work.Active != 0 {
		t.Errorf("work = %+v, want Size 1 for the completed task", work)
	}

	hist, err := c.History(t.Context(), "", 7)
	if err != nil {
		t.Fatal(err)
	}
	today := hist[len(hist)-1]
	if len(hist) != 7 || today.Processed != 2 || today.Failed != 1 || !today.Date.Equal(time.Now().UTC().Truncate(24*time.Hour)) {
		t.Errorf("history = %+v", hist)
	}
	one, err := c.History(t.Context(), "flaky", 7)
	if err != nil || one[6].Processed != 1 || one[6].Failed != 1 {
		t.Errorf("flaky history = %+v, %v", one, err)
	}
	if _, err := c.History(t.Context(), "nope", 7); !errors.Is(err, conn.ErrQueueNotFound) {
		t.Errorf("unknown queue history: %v", err)
	}

	// Pause and resume show at once: the action drops the cache.
	paused := func() bool {
		qs, err := c.Queues(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return qs[0].Paused
	}
	if err := c.PauseQueue(t.Context(), "default"); err != nil {
		t.Fatal(err)
	}
	if !paused() {
		t.Error("default not paused right after PauseQueue")
	}
	if err := c.PauseQueue(t.Context(), "default"); err == nil {
		t.Error("pausing a paused queue succeeded")
	}
	if err := c.UnpauseQueue(t.Context(), "default"); err != nil {
		t.Fatal(err)
	}
	if paused() {
		t.Error("default still paused after UnpauseQueue")
	}
	if err := c.PauseQueue(t.Context(), "nope"); !errors.Is(err, conn.ErrQueueNotFound) {
		t.Errorf("pause unknown: %v", err)
	}

	// Delete refuses a queue with tasks, then deletes an emptied one.
	if err := c.DeleteQueue(t.Context(), "default"); !errors.Is(err, conn.ErrQueueNotEmpty) {
		t.Errorf("delete non-empty: %v", err)
	}
	if err := c.DeleteQueue(t.Context(), "nope"); !errors.Is(err, conn.ErrQueueNotFound) {
		t.Errorf("delete unknown: %v", err)
	}
	if _, err := c.Inspector().DeleteAllScheduledTasks("low"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteQueue(t.Context(), "low"); err != nil {
		t.Fatal(err)
	}
	qs, err = c.Queues(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range qs {
		if q.Name == "low" {
			t.Error("low still listed after DeleteQueue")
		}
	}
}
