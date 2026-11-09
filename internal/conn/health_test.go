package conn

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
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

// lockedBuffer collects log output from several goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func logTo(c *Conn) *lockedBuffer {
	var buf lockedBuffer
	c.logger = slog.New(slog.NewTextHandler(&buf, nil))
	return &buf
}

// pingStep is the answer to one PING, by call order.
type pingStep struct {
	latency time.Duration
	err     error
}

func steppedPinger(steps ...pingStep) pinger {
	var n atomic.Int32
	return pingFunc(func(context.Context) error {
		i := int(n.Add(1)) - 1
		if i >= len(steps) {
			return nil
		}
		time.Sleep(steps[i].latency)
		return steps[i].err
	})
}

// Of two overlapping checks, the one that started last is kept, whether it
// is the loop's or a Recheck's, and whichever finishes first.
func TestOverlappingChecks(t *testing.T) {
	errDial := errors.New("dial tcp: i/o timeout")
	tests := []struct {
		name    string
		steps   []pingStep    // loop at 0s and 10s, Recheck at recheck
		recheck time.Duration // when Retry now is clicked
		at      time.Duration // when the stored health is read
		want    State
		checked time.Duration // the stored CheckedAt, from the start
	}{
		{
			// The loop's check starts while Redis is down and hangs in dial
			// until 9s; Redis is back when Retry now runs at 5s.
			name:    "stale loop check after a newer Recheck",
			steps:   []pingStep{{9 * time.Second, errDial}, {0, nil}},
			recheck: 5 * time.Second,
			at:      9500 * time.Millisecond,
			want:    StateUp,
			checked: 5 * time.Second,
		},
		{
			// Retry now at 8s hangs until 17s; the loop's check at 10s
			// sees Redis up.
			name:    "stale Recheck after a newer loop check",
			steps:   []pingStep{{0, nil}, {9 * time.Second, errDial}, {0, nil}},
			recheck: 8 * time.Second,
			at:      17500 * time.Millisecond,
			want:    StateUp,
			checked: 10 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := testConn(t, "main", steppedPinger(tt.steps...))
				c.timeout = 11 * time.Second
				logs := logTo(c)
				reg := newRegistry(nil, c)
				start := time.Now()
				reg.Start(t.Context())

				time.Sleep(tt.recheck)
				recheck := make(chan Health, 1)
				go func() {
					h, _ := c.Recheck(t.Context())
					recheck <- h
				}()
				time.Sleep(tt.at - tt.recheck)
				synctest.Wait()

				want := Health{State: tt.want, CheckedAt: start.Add(tt.checked)}
				if diff := cmp.Diff(want, c.Health(), cmpopts.EquateErrors()); diff != "" {
					t.Errorf("stored health (-want +got):\n%s", diff)
				}
				if h := <-recheck; h != c.Health() {
					t.Errorf("Recheck returned %+v, stored %+v", h, c.Health())
				}
				if strings.Contains(logs.String(), "unreachable") {
					t.Errorf("a stale result was logged:\n%s", logs)
				}
				if err := reg.Close(); err != nil {
					t.Error(err)
				}
			})
		})
	}
}

func TestCheckAfterClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var pings atomic.Int32
		c := testConn(t, "main", pingFunc(func(context.Context) error {
			pings.Add(1)
			<-release
			return errors.New("redis: client is closed")
		}))
		logs := logTo(c)
		reg := newRegistry(nil, c)

		// A Retry now in flight when Close runs.
		inFlight := make(chan error, 1)
		go func() {
			_, err := reg.Recheck(t.Context(), "main")
			inFlight <- err
		}()
		synctest.Wait()
		if err := reg.Close(); err != nil {
			t.Fatal(err)
		}
		close(release)
		if err := <-inFlight; !errors.Is(err, ErrClosed) {
			t.Errorf("in-flight Recheck error = %v, want ErrClosed", err)
		}

		// One after Close does not PING at all.
		if _, err := reg.Recheck(t.Context(), "main"); !errors.Is(err, ErrClosed) {
			t.Errorf("Recheck after Close error = %v, want ErrClosed", err)
		}
		if n := pings.Load(); n != 1 {
			t.Errorf("%d PINGs, want 1", n)
		}
		if got := c.Health(); got.State != StateUnknown {
			t.Errorf("health = %+v, want still unknown", got)
		}
		if s := logs.String(); s != "" {
			t.Errorf("logged after Close:\n%s", s)
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
