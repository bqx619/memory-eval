package main

import (
	"context"
	"memeval/internal/sandbox/container"
	"sync"
	"time"
)

// The timer records state during long in-flight episodes; the same check also
// runs at reset boundaries so short episodes cannot outrun the five-second poll.
// Monitoring failure is retained and fails closed at the final isolation audit.
func watchGovernor(ctx context.Context, b *container.Backend) (func(), func() error, func() string) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var mu sync.Mutex
	var last error
	check := func() string {
		_, err := b.PollGovernor(ctx)
		if err != nil {
			if ctx.Err() == nil {
				mu.Lock()
				last = err
				mu.Unlock()
			}
			return "exhausted"
		}
		return b.GovernorStop()
	}
	go func() {
		defer close(done)
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				check()
			}
		}
	}()
	return func() { cancel(); <-done }, func() error { mu.Lock(); defer mu.Unlock(); return last }, check
}
