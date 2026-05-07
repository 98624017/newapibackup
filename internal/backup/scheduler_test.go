package backup

import (
	"context"
	"testing"
	"time"
)

func TestRunLoopRunsImmediatelyWhenBackupOnStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0

	err := RunLoop(ctx, Config{Interval: time.Hour, BackupOnStart: true}, func(ctx context.Context, now time.Time) error {
		calls++
		cancel()
		return nil
	}, func(time.Duration) <-chan time.Time {
		t.Fatal("timer should not be used before immediate backup")
		return nil
	})
	if err != nil {
		t.Fatalf("RunLoop returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRunLoopWaitsWhenBackupOnStartDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := make(chan time.Time, 1)
	calls := 0

	go func() {
		timer <- time.Date(2026, 5, 7, 14, 30, 0, 0, time.UTC)
	}()

	err := RunLoop(ctx, Config{Interval: time.Hour, BackupOnStart: false}, func(ctx context.Context, now time.Time) error {
		calls++
		cancel()
		return nil
	}, func(duration time.Duration) <-chan time.Time {
		if duration != time.Hour {
			t.Fatalf("duration = %v, want 1h", duration)
		}
		return timer
	})
	if err != nil {
		t.Fatalf("RunLoop returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRunLoopExitsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := RunLoop(ctx, Config{Interval: time.Hour, BackupOnStart: false}, func(ctx context.Context, now time.Time) error {
		t.Fatal("backup should not run after cancellation")
		return nil
	}, time.After)
	if err != nil {
		t.Fatalf("RunLoop returned error: %v", err)
	}
}
