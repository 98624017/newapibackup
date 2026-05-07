package backup

import (
	"context"
	"log"
	"time"
)

type BackupFunc func(ctx context.Context, now time.Time) error

type AfterFunc func(time.Duration) <-chan time.Time

func RunLoop(ctx context.Context, cfg Config, backup BackupFunc, after AfterFunc) error {
	if cfg.BackupOnStart {
		if err := backup(ctx, time.Now().UTC()); err != nil {
			log.Printf("backup failed: %v", err)
		}
		if ctx.Err() != nil {
			return nil
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-after(cfg.Interval):
			if err := backup(ctx, now.UTC()); err != nil {
				log.Printf("backup failed: %v", err)
			}
		}
	}
}
