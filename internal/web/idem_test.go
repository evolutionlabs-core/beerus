package web

import (
	"sync"
	"testing"
)

func TestIdempotencyLocksExcludeAndAreReleased(t *testing.T) {
	var a Idempotency
	var wg sync.WaitGroup
	inside := map[string]int{}
	var mu sync.Mutex
	for _, scope := range []string{"s1", "s1", "s1", "s2", "s2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := a.Acquire(scope)
			mu.Lock()
			inside[scope]++
			if inside[scope] != 1 {
				t.Errorf("two requests held %s at once", scope)
			}
			mu.Unlock()
			mu.Lock()
			inside[scope]--
			mu.Unlock()
			a.Release(scope, e)
		}()
	}
	wg.Wait()
	if n := len(a.locks); n != 0 {
		t.Fatalf("%d locks left after every request finished", n)
	}
	e := a.Acquire("s3")
	if len(a.locks) != 1 {
		t.Fatalf("held lock missing: %d", len(a.locks))
	}
	a.Release("s3", e)
	if len(a.locks) != 0 {
		t.Fatal("lock not removed on release")
	}
}
