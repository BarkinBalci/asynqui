package conn

import (
	"context"
	"io"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/BarkinBalci/asynqui/internal/config"
)

// hungServer accepts TCP connections and never replies, like a Redis that
// hangs.
func hungServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}()
		}
	}()
	return ln.Addr().String()
}

// Against a real socket that never answers, viewers polling Summary get
// their own timeout, goroutines stay bounded and all end once polling
// stops, and Close is not held up by more than the socket timeouts.
func TestHungRedis(t *testing.T) {
	if testing.Short() {
		t.Skip("takes a few seconds")
	}
	addr := hungServer(t)
	cc := config.Connection{Name: "main", Addr: addr, DialTimeout: 200 * time.Millisecond, ReadTimeout: 300 * time.Millisecond, WriteTimeout: 300 * time.Millisecond}
	reg, err := NewRegistry([]config.Connection{cc}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Get("main")
	base := runtime.NumGoroutine()

	const requestTimeout = 500 * time.Millisecond
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		slowest time.Duration
		peak    int
	)
	for stop := time.Now().Add(3 * time.Second); time.Now().Before(stop); time.Sleep(50 * time.Millisecond) {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
			defer cancel()
			start := time.Now()
			if _, err := c.Summary(ctx); err == nil {
				t.Error("Summary succeeded against a hung server")
			}
			mu.Lock()
			slowest = max(slowest, time.Since(start))
			mu.Unlock()
		})
		peak = max(peak, runtime.NumGoroutine()-base)
	}
	wg.Wait()
	t.Logf("slowest viewer %v, peak %d extra goroutines", slowest, peak)
	if slowest > requestTimeout+200*time.Millisecond {
		t.Errorf("a viewer waited %v, longer than its %v timeout", slowest, requestTimeout)
	}
	if peak > 40 {
		t.Errorf("%d extra goroutines while polling, want a bounded number", peak)
	}
	deadline := time.Now().Add(10 * time.Second)
	for runtime.NumGoroutine() > base+2 {
		if time.Now().After(deadline) {
			t.Fatalf("%d extra goroutines 10s after polling stopped", runtime.NumGoroutine()-base)
		}
		time.Sleep(50 * time.Millisecond)
	}

	reg.Start(context.Background())
	time.Sleep(100 * time.Millisecond) // a PING is in flight
	start := time.Now()
	if err := reg.Close(); err != nil {
		t.Error(err)
	}
	took := time.Since(start)
	t.Logf("Close took %v", took)
	if took > 3*time.Second {
		t.Errorf("Close with a health check in flight took %v", took)
	}
}
