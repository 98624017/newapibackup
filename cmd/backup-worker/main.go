package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/98624017/newapibackup/internal/backup"
)

func main() {
	cfg, err := backup.LoadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := backup.NewR2Client(cfg)
	uploader := backup.NewS3Uploader(client, cfg.R2BucketName)

	log.Printf("backup worker started name=%s interval=%s prefix=%q bucket=%s", cfg.BackupName, cfg.Interval, cfg.R2Prefix, cfg.R2BucketName)
	err = backup.RunLoop(ctx, cfg, func(ctx context.Context, now time.Time) error {
		manifest, err := backup.RunOnce(ctx, cfg, now, backup.DumpPostgres, uploader)
		if err != nil {
			return err
		}
		log.Printf("backup uploaded object=%s size=%d sha256=%s", manifest.Object, manifest.Size, manifest.SHA256)
		return nil
	}, time.After)
	if err != nil {
		log.Fatalf("backup worker stopped with error: %v", err)
	}
	log.Print("backup worker stopped")
}
