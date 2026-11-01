package flight

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentCallsShareOneExecution(t *testing.T) {
	var g Group
	var executions, leaders atomic.Int64
	release := make(chan struct{})
	var started, done sync.WaitGroup
	const n = 20
	results := make([]any, n)

	for i := 0; i < n; i++ {
		started.Add(1)
		done.Add(1)
		go func(i int) {
			defer done.Done()
			started.Done()
			v, leader, err := g.Do("k", func() (any, error) {
				executions.Add(1)
				<-release
				return "shared", nil
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if leader {
				leaders.Add(1)
			}
			results[i] = v
		}(i)
	}
	started.Wait()
	time.Sleep(100 * time.Millisecond) // let every goroutine reach Do while the leader is blocked
	close(release)
	done.Wait()

	if executions.Load() != 1 || leaders.Load() != 1 {
		t.Fatalf("executions=%d leaders=%d, want 1 and 1", executions.Load(), leaders.Load())
	}
	for i, r := range results {
		if r != "shared" {
			t.Errorf("caller %d got %v", i, r)
		}
	}
}

func TestKeyIsReleasedAfterCompletion(t *testing.T) {
	var g Group
	var runs int
	for i := 0; i < 3; i++ {
		_, leader, _ := g.Do("k", func() (any, error) { runs++; return nil, nil })
		if !leader {
			t.Fatal("sequential calls must each lead")
		}
	}
	if runs != 3 {
		t.Fatalf("runs = %d", runs)
	}
}
