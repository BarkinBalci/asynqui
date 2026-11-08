package conn

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// pingFunc is a fake pinger.
type pingFunc func(ctx context.Context) error

func (f pingFunc) Ping(ctx context.Context) error { return f(ctx) }

// pingResult is what the next fake PING returns, after taking latency.
type pingResult struct {
	err     error
	latency time.Duration
}

// scriptedPinger returns a pinger that serves each PING from results.
func scriptedPinger(results <-chan pingResult) pinger {
	return pingFunc(func(ctx context.Context) error {
		select {
		case r := <-results:
			time.Sleep(r.latency)
			return r.err
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

// Tests in a synctest bubble run on a fake clock, and synctest.Test fails if
// any goroutine the test started is still blocked when it returns, so they
// also check that the health loops do not leak.

func TestHealthLoop(t *testing.T) {
	errRefused := errors.New("connection refused")
	errAuth := errors.New("WRONGPASS")
	steps := []struct {
		name   string
		result pingResult
		want   Health // CheckedAt is filled in from the fake clock
	}{
		{"first check up", pingResult{nil, 3 * time.Millisecond}, Health{State: StateUp, Latency: 3 * time.Millisecond}},
		{"goes down", pingResult{errRefused, time.Millisecond}, Health{State: StateDown, Err: errRefused, Latency: time.Millisecond}},
		{"stays down with a new error", pingResult{errAuth, 0}, Health{State: StateDown, Err: errAuth}},
		{"comes back", pingResult{nil, 2 * time.Millisecond}, Health{State: StateUp, Latency: 2 * time.Millisecond}},
	}
	synctest.Test(t, func(t *testing.T) {
		results := make(chan pingResult)
		c := testConn(t, "main", scriptedPinger(results))
		reg := newRegistry(nil, c)
		ctx, cancel := context.WithCancel(t.Context())
		start := time.Now()
		reg.Start(ctx)
		reg.Start(ctx) // a second Start does nothing

		synctest.Wait() // the first PING is in flight
		if got := c.Health(); got.State != StateUnknown || !got.CheckedAt.IsZero() {
			t.Fatalf("health before the first check = %+v, want unknown", got)
		}

		for i, step := range steps {
			if i > 0 {
				// The next check starts one interval after the previous one.
				time.Sleep(time.Until(start.Add(time.Duration(i) * healthInterval)))
			}
			results <- step.result
			time.Sleep(step.result.latency) // the PING takes its latency
			synctest.Wait()
			want := step.want
			want.CheckedAt = time.Now()
			if diff := cmp.Diff(want, c.Health(), cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s: health (-want +got):\n%s", step.name, diff)
			}
		}

		cancel()
		synctest.Wait() // the loop has returned
		if err := reg.Close(); err != nil {
			t.Error(err)
		}
	})
}

func TestHealthCheckTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testConn(t, "main", pingFunc(func(ctx context.Context) error {
			<-ctx.Done() // Redis never answers
			return ctx.Err()
		}))
		c.timeout = 2 * time.Second

		got, err := c.Recheck(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want := Health{State: StateDown, Err: context.DeadlineExceeded, CheckedAt: time.Now(), Latency: 2 * time.Second}
		if diff := cmp.Diff(want, got, cmpopts.EquateErrors()); diff != "" {
			t.Errorf("Recheck (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(want, c.Health(), cmpopts.EquateErrors()); diff != "" {
			t.Errorf("stored health (-want +got):\n%s", diff)
		}
	})
}

func TestRecheckCanceledStoresNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testConn(t, "main", pingFunc(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}))
		ctx, cancel := context.WithTimeout(t.Context(), time.Second) // shorter than c.timeout
		defer cancel()
		if _, err := c.Recheck(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Recheck error = %v, want the context's error", err)
		}
		if got := c.Health(); got.State != StateUnknown {
			t.Errorf("health = %+v, want unknown", got)
		}
	})
}

func TestRecheckByName(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pings := 0
		c := testConn(t, "main", pingFunc(func(context.Context) error {
			pings++
			return nil
		}))
		reg := newRegistry(nil, c)
		got, err := reg.Recheck(t.Context(), "main")
		if err != nil {
			t.Fatal(err)
		}
		if got.State != StateUp || pings != 1 || c.Health() != got {
			t.Errorf("Recheck = %+v after %d pings, stored %+v", got, pings, c.Health())
		}
	})
}

func TestCloseStopsHealthLoops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var a, b atomic.Int32
		count := func(n *atomic.Int32) pinger {
			return pingFunc(func(context.Context) error {
				n.Add(1)
				return nil
			})
		}
		reg := newRegistry(nil, testConn(t, "a", count(&a)), testConn(t, "b", count(&b)))
		reg.Start(t.Context()) // never canceled: Close must stop the loops
		time.Sleep(25 * time.Second)
		synctest.Wait()
		if err := reg.Close(); err != nil {
			t.Fatal(err)
		}
		// Checks at 0s, 10s and 20s.
		if a.Load() != 3 || b.Load() != 3 {
			t.Errorf("pings = %d and %d, want 3 each", a.Load(), b.Load())
		}
		reg.Start(t.Context()) // after Close, Start does nothing
		time.Sleep(time.Minute)
		if a.Load() != 3 {
			t.Errorf("Start after Close ran checks")
		}
	})
}

func TestStateString(t *testing.T) {
	for s, want := range map[State]string{StateUnknown: "unknown", StateUp: "up", StateDown: "down", State(9): "unknown"} {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", int(s), got, want)
		}
	}
}
