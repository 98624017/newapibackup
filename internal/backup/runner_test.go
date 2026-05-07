package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testConfig(stateDir string) Config {
	return Config{
		DatabaseURL:   "postgres://example",
		BackupName:    "newapi",
		Interval:      12 * time.Hour,
		BackupOnStart: true,
		R2AccountID:   "account",
		R2AccessKeyID: "key",
		R2SecretKey:   "secret",
		R2BucketName:  "bucket",
		R2Prefix:      "newapi",
		StateDir:      stateDir,
	}
}

func TestRunOnceUploadsBackupManifestAndLatest(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	now := time.Date(2026, 5, 7, 14, 30, 0, 0, time.UTC)
	uploads := make([]string, 0)

	manifest, err := RunOnce(
		context.Background(),
		cfg,
		now,
		func(ctx context.Context, databaseURL string, outputPath string) error {
			if databaseURL != cfg.DatabaseURL {
				t.Fatalf("databaseURL = %q, want config URL", databaseURL)
			}
			return os.WriteFile(outputPath, []byte("backup"), 0o600)
		},
		func(ctx context.Context, key string, path string, contentType string) error {
			uploads = append(uploads, key)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("uploaded path %s missing: %v", path, err)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}

	wantBackup := "newapi/full/2026/05/newapi-backup-20260507-143000.sql.gz"
	wantManifest := wantBackup + ".json"
	if manifest.Object != wantBackup {
		t.Fatalf("manifest.Object = %q, want %q", manifest.Object, wantBackup)
	}
	if manifest.Size != 6 {
		t.Fatalf("manifest.Size = %d, want 6", manifest.Size)
	}
	if uploads[0] != wantBackup || uploads[1] != wantManifest || uploads[2] != "newapi/full/latest.json" {
		t.Fatalf("uploads = %#v, want backup, manifest, latest", uploads)
	}
	if entries, err := os.ReadDir(tmp); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("state dir has leftover files: %#v", entries)
	}
}

func TestRunOnceCleansTempFilesAfterUploadFailure(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	now := time.Date(2026, 5, 7, 14, 30, 0, 0, time.UTC)

	_, err := RunOnce(
		context.Background(),
		cfg,
		now,
		func(ctx context.Context, databaseURL string, outputPath string) error {
			return os.WriteFile(outputPath, []byte("backup"), 0o600)
		},
		func(ctx context.Context, key string, path string, contentType string) error {
			return errors.New("upload failed")
		},
	)
	if err == nil || err.Error() != "upload failed" {
		t.Fatalf("err = %v, want upload failed", err)
	}
	if matches, err := filepath.Glob(filepath.Join(tmp, "*")); err != nil {
		t.Fatal(err)
	} else if len(matches) != 0 {
		t.Fatalf("state dir has leftover files: %#v", matches)
	}
}
