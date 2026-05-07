package backup

import (
	"testing"
	"time"
)

func TestBuildBackupKeyUsesUTCDatePath(t *testing.T) {
	createdAt := time.Date(2026, 5, 7, 14, 30, 0, 0, time.UTC)

	got := BuildBackupKey("newapi", createdAt)
	want := "full/2026/05/newapi-backup-20260507-143000.sql.gz"
	if got != want {
		t.Fatalf("BuildBackupKey = %q, want %q", got, want)
	}
}

func TestJoinPrefixCleansSlashes(t *testing.T) {
	got := JoinPrefix("/newapi/", "/full/latest.json")
	want := "newapi/full/latest.json"
	if got != want {
		t.Fatalf("JoinPrefix = %q, want %q", got, want)
	}
}

func TestJoinPrefixAllowsEmptyPrefix(t *testing.T) {
	got := JoinPrefix("", "full/latest.json")
	want := "full/latest.json"
	if got != want {
		t.Fatalf("JoinPrefix = %q, want %q", got, want)
	}
}

func TestNewManifestRecordsRestoreMetadata(t *testing.T) {
	createdAt := time.Date(2026, 5, 7, 14, 30, 0, 0, time.UTC)

	manifest := NewManifest("newapi", createdAt, "newapi/full/backup.sql.gz", "abc123", 42)

	if manifest.SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d, want 1", manifest.SchemaVersion)
	}
	if manifest.Name != "newapi" {
		t.Fatalf("Name = %q, want newapi", manifest.Name)
	}
	if manifest.CreatedAt != "2026-05-07T14:30:00Z" {
		t.Fatalf("CreatedAt = %q, want RFC3339 UTC", manifest.CreatedAt)
	}
	if manifest.Object != "newapi/full/backup.sql.gz" {
		t.Fatalf("Object = %q, want object key", manifest.Object)
	}
	if manifest.SHA256 != "abc123" {
		t.Fatalf("SHA256 = %q, want abc123", manifest.SHA256)
	}
	if manifest.Size != 42 {
		t.Fatalf("Size = %d, want 42", manifest.Size)
	}
	if manifest.PGDump.Format != "plain" || !manifest.PGDump.Clean || !manifest.PGDump.IfExists {
		t.Fatalf("PGDump metadata = %+v, want plain clean if-exists", manifest.PGDump)
	}
}
