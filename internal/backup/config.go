package backup

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBackupName      = "backup"
	defaultIntervalSeconds = 43200
	defaultStateDir        = "/tmp/backup-worker"
)

type Config struct {
	DatabaseURL   string
	BackupName    string
	Interval      time.Duration
	BackupOnStart bool
	R2AccountID   string
	R2AccessKeyID string
	R2SecretKey   string
	R2BucketName  string
	R2Prefix      string
	StateDir      string
}

func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		BackupName:    defaultBackupName,
		Interval:      time.Duration(defaultIntervalSeconds) * time.Second,
		BackupOnStart: true,
		StateDir:      defaultStateDir,
	}

	required := map[string]*string{
		"DATABASE_URL":         &cfg.DatabaseURL,
		"R2_ACCOUNT_ID":        &cfg.R2AccountID,
		"R2_ACCESS_KEY_ID":     &cfg.R2AccessKeyID,
		"R2_SECRET_ACCESS_KEY": &cfg.R2SecretKey,
		"R2_BUCKET_NAME":       &cfg.R2BucketName,
	}
	for key, target := range required {
		value := strings.TrimSpace(getenv(key))
		if value == "" {
			return Config{}, fmt.Errorf("missing required env var: %s", key)
		}
		*target = value
	}

	if value := strings.TrimSpace(getenv("BACKUP_NAME")); value != "" {
		cfg.BackupName = value
	}
	if value := strings.TrimSpace(getenv("BACKUP_INTERVAL_SECONDS")); value != "" {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds <= 0 {
			return Config{}, fmt.Errorf("BACKUP_INTERVAL_SECONDS must be a positive integer")
		}
		cfg.Interval = time.Duration(seconds) * time.Second
	}
	if value := strings.TrimSpace(getenv("BACKUP_ON_START")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("BACKUP_ON_START must be true or false")
		}
		cfg.BackupOnStart = parsed
	}
	cfg.R2Prefix = strings.Trim(strings.TrimSpace(getenv("R2_PREFIX")), "/")
	if value := strings.TrimSpace(getenv("BACKUP_STATE_DIR")); value != "" {
		cfg.StateDir = value
	}

	return cfg, nil
}
