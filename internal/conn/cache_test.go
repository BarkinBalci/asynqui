package conn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestCachedTTL(t *testing.T) {
	steps := []struct {
		after       time.Duration // since the previous step
		key         cacheKey[int32]
		wantFetches int32
	}{
		{0, "a", 1},
		{0, "a", 1},
		{999 * time.Millisecond, "a", 1},
		{0, "b", 2},                // keys are independent
		{time.Millisecond, "a", 3}, // a expired 1s after its fetch
		{500 * time.Millisecond, "a", 3},
		{500 * time.Millisecond, "b", 4},
	}
	synctest.Test(t, func(t *testing.T) {
		c := newCache()
		var fetches atomic.Int32
		fetch := func(context.Context) (int32, error) { return fetches.Add(1), nil }
		for i, s := range steps {
			time.Sleep(s.after)
			if _, err := cached(t.Context(), c, s.key, time.Second, fetch); err != nil {
				t.Fatalf("step %d: %v", i, err)
			}
			if n := fetches.Load(); n != s.wantFetches {
				t.Errorf("step %d: %d fetches, want %d", i, n, s.wantFetches)
			}
		}
	})
}

func TestCachedCoalesces(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCache()
		var fetches atomic.Int32
		release := make(chan struct{})
		fetch := func(context.Context) (string, error) { //nolint:unparam // the signature cached takes
			fetches.Add(1)
			<-release
			return "snapshot", nil
		}

		const viewers = 50
		got := make([]string, viewers)
		var wg sync.WaitGroup
		for i := range viewers {
			wg.Go(func() {
				v, err := cached(t.Context(), c, "summary", time.Second, fetch)
				if err != nil {
					t.Error(err)
				}
				got[i] = v
			})
		}
		synctest.Wait() // every viewer waits on the one fetch
		close(release)
		wg.Wait()

		if n := fetches.Load(); n != 1 {
			t.Errorf("%d fetches for %d concurrent viewers, want 1", n, viewers)
		}
		for i, v := range got {
			if v != "snapshot" {
				t.Errorf("viewer %d got %q", i, v)
			}
		}
	})
}

func TestCachedDoesNotCacheErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCache()
		errDown := errors.New("down")
		var fetches atomic.Int32
		fetch := func(context.Context) (int, error) {
			if fetches.Add(1) == 1 {
				return 0, errDown
			}
			return 7, nil
		}
		if _, err := cached(t.Context(), c, "k", time.Second, fetch); !errors.Is(err, errDown) {
			t.Fatalf("first call error = %v, want %v", err, errDown)
		}
		v, err := cached(t.Context(), c, "k", time.Second, fetch)
		if err != nil || v != 7 {
			t.Fatalf("second call = %v, %v, want 7, nil", v, err)
		}
		if n := fetches.Load(); n != 2 {
			t.Errorf("%d fetches, want 2", n)
		}
	})
}

func TestCachedCallerCanceled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCache()
		release := make(chan struct{})
		var fetches atomic.Int32
		var fetchErr error
		fetch := func(ctx context.Context) (string, error) { //nolint:unparam // the signature cached takes
			fetches.Add(1)
			<-release
			fetchErr = ctx.Err()
			return "snapshot", nil
		}

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		go func() {
			_, err := cached(ctx, c, "k", time.Second, fetch)
			done <- err
		}()
		synctest.Wait()
		cancel() // the first viewer leaves
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled caller error = %v, want context.Canceled", err)
		}

		close(release)
		synctest.Wait()
		// The fetch finished for the others, uncanceled, and was cached.
		v, err := cached(t.Context(), c, "k", time.Second, fetch)
		if err != nil || v != "snapshot" {
			t.Fatalf("next caller = %q, %v", v, err)
		}
		if fetchErr != nil {
			t.Errorf("fetch saw ctx error %v", fetchErr)
		}
		if n := fetches.Load(); n != 1 {
			t.Errorf("%d fetches, want 1", n)
		}
	})
}

func TestCachedKeyTypes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The same name with different value types is two keys.
		c := newCache()
		n, err := cached(t.Context(), c, cacheKey[int]("k"), time.Second, func(context.Context) (int, error) { return 1, nil })
		if err != nil || n != 1 {
			t.Fatalf("int key = %v, %v", n, err)
		}
		s, err := cached(t.Context(), c, cacheKey[string]("k"), time.Second, func(context.Context) (string, error) { return "one", nil })
		if err != nil || s != "one" {
			t.Fatalf("string key = %q, %v", s, err)
		}
	})
}

func TestCachedRecoversPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCache()
		var calls atomic.Int32
		fetch := func(context.Context) (int, error) {
			if calls.Add(1) == 1 {
				panic("bad reply")
			}
			return 2, nil
		}
		_, err := cached(t.Context(), c, "k", time.Second, fetch)
		if err == nil || !strings.Contains(err.Error(), "panic: bad reply") {
			t.Fatalf("error = %v, want the panic", err)
		}
		// The panic is not cached and the key is usable again.
		if v, err := cached(t.Context(), c, "k", time.Second, fetch); err != nil || v != 2 {
			t.Fatalf("after the panic = %v, %v", v, err)
		}
	})
}

// Keys that are never asked for again (a deleted queue's, say) must not pile
// up.
func TestCacheSweepsExpiredEntries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCache()
		fetch := func(context.Context) ([]byte, error) { return make([]byte, 1024), nil }
		for i := range 10000 {
			if _, err := cached(t.Context(), c, cacheKey[[]byte](fmt.Sprintf("tasks:q%d", i)), time.Second, fetch); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(time.Hour)
		if _, err := cached(t.Context(), c, "summary", time.Second, func(context.Context) (int, error) { return 1, nil }); err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		n, flights := len(c.entries), len(c.flights)
		c.mu.Unlock()
		if n != 1 || flights != 0 {
			t.Errorf("%d entries and %d flights after the others expired, want 1 and 0", n, flights)
		}
	})
}
