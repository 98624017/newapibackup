package backup

import (
	"strings"
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":         "postgres://user:pass@postgres.internal:5432/newapi",
		"R2_ACCOUNT_ID":        "account-id",
		"R2_ACCESS_KEY_ID":     "access-key",
		"R2_SECRET_ACCESS_KEY": "secret-key",
		"R2_BUCKET_NAME":       "bucket",
	}
}

func TestLoadConfigUsesDefaults(t *testing.T) {
	cfg, err := LoadConfig(env(validEnv()))
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.BackupName != "backup" {
		t.Fatalf("BackupName = %q, want backup", cfg.BackupName)
	}
	if cfg.Interval != 12*time.Hour {
		t.Fatalf("Interval = %v, want 12h", cfg.Interval)
	}
	if !cfg.BackupOnStart {
		t.Fatal("BackupOnStart = false, want true")
	}
	if cfg.R2Prefix != "" {
		t.Fatalf("R2Prefix = %q, want empty", cfg.R2Prefix)
	}
	if cfg.StateDir != "/tmp/backup-worker" {
		t.Fatalf("StateDir = %q, want /tmp/backup-worker", cfg.StateDir)
	}
}

func TestLoadConfigReadsOverrides(t *testing.T) {
	values := validEnv()
	values["BACKUP_NAME"] = "newapi"
	values["BACKUP_INTERVAL_SECONDS"] = "7200"
	values["BACKUP_ON_START"] = "false"
	values["R2_PREFIX"] = "/newapi/"
	values["BACKUP_STATE_DIR"] = "/data/backups"

	cfg, err := LoadConfig(env(values))
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.BackupName != "newapi" {
		t.Fatalf("BackupName = %q, want newapi", cfg.BackupName)
	}
	if cfg.Interval != 2*time.Hour {
		t.Fatalf("Interval = %v, want 2h", cfg.Interval)
	}
	if cfg.BackupOnStart {
		t.Fatal("BackupOnStart = true, want false")
	}
	if cfg.R2Prefix != "newapi" {
		t.Fatalf("R2Prefix = %q, want newapi", cfg.R2Prefix)
	}
	if cfg.StateDir != "/data/backups" {
		t.Fatalf("StateDir = %q, want /data/backups", cfg.StateDir)
	}
}

func TestLoadConfigRequiresDatabaseURL(t *testing.T) {
	values := validEnv()
	delete(values, "DATABASE_URL")

	_, err := LoadConfig(env(values))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("err = %v, want DATABASE_URL error", err)
	}
}

func TestLoadConfigRejectsInvalidInterval(t *testing.T) {
	values := validEnv()
	values["BACKUP_INTERVAL_SECONDS"] = "0"

	_, err := LoadConfig(env(values))
	if err == nil || !strings.Contains(err.Error(), "BACKUP_INTERVAL_SECONDS") {
		t.Fatalf("err = %v, want interval error", err)
	}
}

func TestLoadConfigRejectsInvalidBackupOnStart(t *testing.T) {
	values := validEnv()
	values["BACKUP_ON_START"] = "maybe"

	_, err := LoadConfig(env(values))
	if err == nil || !strings.Contains(err.Error(), "BACKUP_ON_START") {
		t.Fatalf("err = %v, want BACKUP_ON_START error", err)
	}
}
