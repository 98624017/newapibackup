package backup

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Dumper func(ctx context.Context, databaseURL string, outputPath string) error

type Uploader func(ctx context.Context, key string, path string, contentType string) error

func RunOnce(ctx context.Context, cfg Config, now time.Time, dumper Dumper, uploader Uploader) (Manifest, error) {
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return Manifest{}, err
	}

	backupKey := BuildBackupKey(cfg.BackupName, now)
	objectKey := JoinPrefix(cfg.R2Prefix, backupKey)
	localBackup := filepath.Join(cfg.StateDir, filepath.Base(backupKey))
	manifestPath := localBackup + ".json"
	latestPath := filepath.Join(cfg.StateDir, "latest.json")
	defer cleanup(localBackup, manifestPath, latestPath)

	if err := dumper(ctx, cfg.DatabaseURL, localBackup); err != nil {
		return Manifest{}, err
	}

	sum, err := sha256File(localBackup)
	if err != nil {
		return Manifest{}, err
	}
	info, err := os.Stat(localBackup)
	if err != nil {
		return Manifest{}, err
	}

	manifest := NewManifest(cfg.BackupName, now, objectKey, sum, info.Size())
	if err := writeJSON(manifestPath, manifest); err != nil {
		return Manifest{}, err
	}
	if err := writeJSON(latestPath, manifest); err != nil {
		return Manifest{}, err
	}

	if err := uploader(ctx, objectKey, localBackup, "application/gzip"); err != nil {
		return Manifest{}, err
	}
	if err := uploader(ctx, objectKey+".json", manifestPath, "application/json"); err != nil {
		return Manifest{}, err
	}
	if err := uploader(ctx, JoinPrefix(cfg.R2Prefix, "full/latest.json"), latestPath, "application/json"); err != nil {
		return Manifest{}, err
	}

	return manifest, nil
}

func DumpPostgres(ctx context.Context, databaseURL string, outputPath string) error {
	cmd := exec.CommandContext(ctx, "pg_dump", databaseURL, "--format=plain", "--no-owner", "--no-acl", "--clean", "--if-exists")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr

	file, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	gzipWriter := gzip.NewWriter(file)
	defer gzipWriter.Close()

	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := io.Copy(gzipWriter, stdout); err != nil {
		_ = cmd.Wait()
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		_ = cmd.Wait()
		return err
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("pg_dump failed: %w", err)
	}
	return nil
}

func cleanup(paths ...string) {
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
