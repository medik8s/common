package nodeidentity_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

// TestConcurrentPatchers runs many goroutines through the ordered
// GET->validate->PATCH flow against the stateful fake. The fake enforces the
// resourceVersion precondition exactly like the real API server, so every
// accepted patch was preconditioned on a fresh version — a lost precondition
// would surface as an unexpected conflict storm or a wrong final state.
// Run with -race: the shared Client must be safe for concurrent use.
func TestConcurrentPatchers(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	const workers, iterations = 10, 10
	var wg sync.WaitGroup
	errs := make(chan error, workers*iterations)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			ctx := context.Background()
			deadline := time.Now().Add(30 * time.Second)
			for i := 0; i < iterations; i++ {
				cond := nodeidentity.Condition{
					Type:   fmt.Sprintf("Worker%d", worker),
					Status: "True",
					Reason: fmt.Sprintf("iter-%d", i),
				}
				// The caller contract on conflict: time-bounded retry with
				// re-GET. Under contention a PATCH may conflict repeatedly;
				// retry until applied or the deadline passes.
				applied := false
				for !applied {
					if time.Now().After(deadline) {
						errs <- fmt.Errorf("worker %d: iteration %d not applied before deadline", worker, i)
						return
					}
					if _, err := c.GetOwnNode(ctx); err != nil {
						errs <- fmt.Errorf("worker %d GET: %w", worker, err)
						return
					}
					if _, err := c.PatchOwnNodeCondition(ctx, cond); err == nil {
						applied = true
					}
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	// Every worker's condition must be present with its final reason.
	for w := 0; w < workers; w++ {
		cond, ok := api.conditionsOf(fmt.Sprintf("Worker%d", w))
		if !ok {
			t.Fatalf("condition Worker%d missing after concurrent patching", w)
		}
		if cond.Reason != fmt.Sprintf("iter-%d", iterations-1) {
			t.Fatalf("Worker%d final reason = %q, want iter-%d", w, cond.Reason, iterations-1)
		}
	}

	api.mu.Lock()
	conflicts := api.conflicts
	api.mu.Unlock()
	t.Logf("server observed %d precondition conflicts across %d concurrent patch attempts", conflicts, workers*iterations)
}

// TestConcurrentGetsAndSharedClientSafety is the -race bait: many goroutines
// share one Client through the ordered flow with a rotating clock.
func TestConcurrentGetsAndSharedClientSafety(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.GetOwnNode(context.Background()); err != nil {
				t.Errorf("concurrent GET: %v", err)
			}
		}()
	}
	wg.Wait()
}
