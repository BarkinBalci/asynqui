package conn_test

import (
	"context"
	"errors"
	"slices"
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
// when no Docker provider is available.

const redisImage = "redis:7.4-alpine"

// runRedis starts a container and returns it with its host:port.
func runRedis(t *testing.T, image string, opts ...testcontainers.ContainerCustomizer) (*tcredis.RedisContainer, string) {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
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

// newClient returns an asynq Client for enqueuing test tasks: asynqui itself
// never enqueues.
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
		// Valkey writes server_mode unless extended-redis-compatibility
		// is on, which it is not by default.
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
			var names []string
			for _, s := range info.Sections {
				names = append(names, s.Name)
			}
			for _, want := range []string{"Server", "Clients", "Memory", "Keyspace"} {
				if !slices.Contains(names, want) {
					t.Errorf("sections %q lack %q", names, want)
				}
			}
			server, _ := info.Section("server")
			if mode, _ := server.Get(tt.modeKey); mode != "standalone" {
				t.Errorf("%s = %q, want standalone", tt.modeKey, mode)
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
