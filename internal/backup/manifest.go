package backup

import (
	"strings"
	"time"
)

type Manifest struct {
	SchemaVersion int            `json:"schema_version"`
	Name          string         `json:"name"`
	CreatedAt     string         `json:"created_at"`
	Object        string         `json:"object"`
	SHA256        string         `json:"sha256"`
	Size          int64          `json:"size"`
	Format        string         `json:"format"`
	PGDump        PGDumpMetadata `json:"pg_dump"`
}

type PGDumpMetadata struct {
	Format   string `json:"format"`
	NoOwner  bool   `json:"no_owner"`
	NoACL    bool   `json:"no_acl"`
	Clean    bool   `json:"clean"`
	IfExists bool   `json:"if_exists"`
}

func BuildBackupKey(name string, createdAt time.Time) string {
	utc := createdAt.UTC()
	return "full/" + utc.Format("2006/01/") + name + "-backup-" + utc.Format("20060102-150405") + ".sql.gz"
}

func JoinPrefix(prefix, key string) string {
	cleanPrefix := strings.Trim(strings.TrimSpace(prefix), "/")
	cleanKey := strings.TrimLeft(key, "/")
	if cleanPrefix == "" {
		return cleanKey
	}
	return cleanPrefix + "/" + cleanKey
}

func NewManifest(name string, createdAt time.Time, objectKey string, sha256 string, size int64) Manifest {
	return Manifest{
		SchemaVersion: 1,
		Name:          name,
		CreatedAt:     createdAt.UTC().Format(time.RFC3339),
		Object:        objectKey,
		SHA256:        sha256,
		Size:          size,
		Format:        "plain sql gzip",
		PGDump: PGDumpMetadata{
			Format:   "plain",
			NoOwner:  true,
			NoACL:    true,
			Clean:    true,
			IfExists: true,
		},
	}
}
