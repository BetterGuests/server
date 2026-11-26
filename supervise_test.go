package main

import (
	"context"
	"testing"
	"time"
)

func TestSuperviseJobRestartsAfterPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runs := 0
	done := make(chan struct{})
	go func() {
		superviseJob(ctx, "flaky", func(context.Context) {
			runs++
			if runs == 1 {
				panic("first run fails")
			}
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * jobRestartFirst):
		t.Fatal("supervisor did not restart the job after a panic")
	}
	if runs != 2 {
		t.Fatalf("expected the job to run twice, got %d", runs)
	}
}

func TestSuperviseJobStopsWhenShuttingDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		superviseJob(ctx, "always panics", func(context.Context) { panic("boom") })
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor kept restarting after shutdown")
	}
}
