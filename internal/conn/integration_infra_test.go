package conn_test

import (
	"context"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/BarkinBalci/asynqui/internal/conn"
)

// TestIntegrationServersAndSchedulers runs an asynq server, whose handler
// holds its task until the test ends, and a scheduler, then reads them.
func TestIntegrationServersAndSchedulers(t *testing.T) {
	_, addr := runRedis(t, redisImage)
	cc := connection("main", addr, 2)
	reg := newRegistry(t, cc)
	c := mustGet(t, reg, "main")
	opt := asynq.RedisClientOpt{Addr: addr, DB: 2}

	release := make(chan struct{})
	srv := asynq.NewServer(opt, asynq.Config{Concurrency: 3, Queues: map[string]int{"reports": 2, "default": 1}, LogLevel: asynq.FatalLevel})
	mux := asynq.NewServeMux()
	mux.HandleFunc("report:daily", func(ctx context.Context, _ *asynq.Task) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	if err := srv.Start(mux); err != nil {
		t.Fatal(err)
	}
	// The scheduler writes its entries on a heartbeat, so beat every second.
	sch := asynq.NewScheduler(opt, &asynq.SchedulerOpts{LogLevel: asynq.FatalLevel, HeartbeatInterval: time.Second})
	id, err := sch.Register("@every 1s", asynq.NewTask("report:daily", []byte(`{"a":1}`)), asynq.Queue("reports"), asynq.MaxRetry(2))
	if err != nil {
		t.Fatal(err)
	}
	if err := sch.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(release)
		sch.Shutdown()
		srv.Shutdown()
	})

	var servers []conn.Server
	eventually(t, "a busy server", func() bool {
		servers, err = c.Servers(t.Context())
		return err == nil && len(servers) == 1 && len(servers[0].Workers) > 0
	})
	s := servers[0]
	if s.Concurrency != 3 || s.Status != "active" || len(s.Queues) != 2 || s.Queues[0] != (conn.QueueWeight{Name: "reports", Weight: 2}) {
		t.Errorf("server = %+v", s)
	}
	if w := s.Workers[0]; w.TaskType != "report:daily" || w.Queue != "reports" || w.Started.IsZero() {
		t.Errorf("worker = %+v", w)
	}

	var entries []conn.SchedulerEntry
	eventually(t, "the scheduler entry", func() bool {
		entries, err = c.SchedulerEntries(t.Context())
		return err == nil && len(entries) > 0
	})
	if len(entries) != 1 || entries[0].ID != id || entries[0].Queue != "reports" || entries[0].Spec != "@every 1s" ||
		string(entries[0].Payload) != `{"a":1}` || len(entries[0].Options) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	var page conn.EnqueuePage
	eventually(t, "two enqueue events", func() bool {
		page, err = c.SchedulerEnqueueEvents(t.Context(), id, "reports", 1, 1)
		return err == nil && page.HasNext
	})
	if len(page.Events) != 1 || !page.Events[0].TaskExists {
		t.Errorf("first page = %+v", page)
	}

	// The keyspace is this connection's DB.
	info, err := c.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if info.Keyspace.DB != 2 || info.Keyspace.Keys == 0 || info.Mode != "standalone" || info.UsedMemory == 0 {
		t.Errorf("info = %+v", info.Keyspace)
	}
}

// TestIntegrationBackendDetection: the health loop detects the backend
// without an Info read.
func TestIntegrationBackendDetection(t *testing.T) {
	for _, tt := range []struct {
		image string
		want  conn.Backend
	}{{redisImage, conn.BackendRedis}, {"valkey/valkey:8.0-alpine", conn.BackendValkey}} {
		t.Run(tt.image, func(t *testing.T) {
			_, addr := runRedis(t, tt.image)
			reg := newRegistry(t, connection("main", addr, 0))
			c := mustGet(t, reg, "main")
			reg.Start(t.Context())
			eventually(t, "backend detection", func() bool { _, known := c.Backend(); return known })
			if b, _ := c.Backend(); b != tt.want {
				t.Errorf("backend = %v, want %v", b, tt.want)
			}
		})
	}
}
