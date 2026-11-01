package load_test

import (
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/load"
)

func TestCounterConcurrentIncDec(t *testing.T) {
	var counter load.Counter
	const workers = 32
	const iterations = 500
	var wg sync.WaitGroup
	wg.Add(workers)
	peak := make(chan int, workers*iterations)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				counter.Inc()
				peak <- counter.Value()
				counter.Dec()
			}
		}()
	}
	wg.Wait()
	close(peak)
	if got := counter.Value(); got != 0 {
		t.Fatalf("counter = %d after balanced inc/dec, want 0", got)
	}
	max := 0
	for value := range peak {
		if value > max {
			max = value
		}
	}
	if max == 0 {
		t.Fatal("counter never observed an in-flight value")
	}
}

func TestNilCounterIsSafe(t *testing.T) {
	var counter *load.Counter
	counter.Inc()
	counter.Dec()
	if got := counter.Value(); got != 0 {
		t.Fatalf("nil counter value = %d, want 0", got)
	}
}
