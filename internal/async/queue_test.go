package async

import (
	"sync/atomic"
	"testing"
)

func TestQueueRunsJobsAndDrainsOnClose(t *testing.T) {
	q := NewQueue(16, 2)
	var ran atomic.Int64
	for i := 0; i < 10; i++ {
		if !q.Submit(func() { ran.Add(1) }) {
			t.Fatal("submit should succeed with free capacity")
		}
	}
	q.Close()
	if ran.Load() != 10 {
		t.Fatalf("ran %d of 10 jobs", ran.Load())
	}
	if q.Submit(func() {}) {
		t.Fatal("submit after close must fail")
	}
}

func TestQueueDropsWhenFull(t *testing.T) {
	q := NewQueue(1, 1)
	started, release := make(chan struct{}), make(chan struct{})
	q.Submit(func() { close(started); <-release })
	<-started // the single worker is now busy
	if !q.Submit(func() {}) {
		t.Fatal("the buffer slot should accept one job")
	}
	if q.Submit(func() {}) {
		t.Fatal("a full queue must reject work instead of blocking")
	}
	if q.Dropped() < 1 {
		t.Fatalf("dropped = %d", q.Dropped())
	}
	close(release)
	q.Close()
}

func TestWorkerSurvivesPanickingJob(t *testing.T) {
	q := NewQueue(4, 1)
	var ran atomic.Int64
	q.Submit(func() { panic("boom") })
	q.Submit(func() { ran.Add(1) })
	q.Close()
	if ran.Load() != 1 {
		t.Fatal("jobs after a panicking job must still run")
	}
}
