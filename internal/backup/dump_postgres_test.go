package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func installDumpFixture(t *testing.T, input string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pg_dump fixture uses a POSIX shell")
	}
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.sql")
	if err := os.WriteFile(inputPath, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"dump_utf8=false\nfor arg do\n  [ \"$arg\" = --encoding=UTF8 ] && dump_utf8=true\ndone\n" +
		"[ \"$dump_utf8\" = true ] || exit 2\n" +
		"cat \"$BACKUP_TEST_DUMP_FILE\" || exit 1\n" +
		"if [ \"$BACKUP_TEST_DUMP_KEEP_WRITING\" = true ]; then exec yes invalid; fi\n" +
		"exit \"$BACKUP_TEST_DUMP_EXIT_CODE\"\n"
	if err := os.WriteFile(filepath.Join(dir, "pg_dump"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BACKUP_TEST_DUMP_FILE", inputPath)
	t.Setenv("BACKUP_TEST_DUMP_EXIT_CODE", "0")
	t.Setenv("BACKUP_TEST_DUMP_KEEP_WRITING", "false")
}

func TestDumpPostgresPublishesFilteredSnapshot(t *testing.T) {
	input := logsDataSection + "COPY public.logs (id, other) FROM stdin;\n1\t" +
		`{"po":["private prompt"],"note":"café","quota":9007199254740993}` + "\n\\.\n"
	want := strings.Replace(input,
		`{"po":["private prompt"],"note":"café","quota":9007199254740993}`,
		`{"note":"café","quota":9007199254740993}`, 1)
	installDumpFixture(t, input)
	cfg := testConfig(t.TempDir())
	var compressed []byte
	var published []Manifest
	uploads := 0
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	manifest, err := RunOnce(ctx, cfg, time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), DumpPostgres,
		func(_ context.Context, _ string, path, contentType string) error {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			uploads++
			if contentType == "application/gzip" {
				compressed = data
				return nil
			}
			var metadata Manifest
			if err := json.Unmarshal(data, &metadata); err != nil {
				return err
			}
			published = append(published, metadata)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	restoredSQL, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if string(restoredSQL) != want {
		t.Fatalf("gzip SQL = %q, want %q", restoredSQL, want)
	}
	digest := sha256.Sum256(compressed)
	wantHash := hex.EncodeToString(digest[:])
	if manifest.Size != int64(len(compressed)) || manifest.SHA256 != wantHash {
		t.Fatalf("manifest does not describe filtered gzip: %+v", manifest)
	}
	if uploads != 3 || len(published) != 2 {
		t.Fatalf("uploads = %d, JSON manifests = %d", uploads, len(published))
	}
	for _, metadata := range published {
		if metadata.SHA256 != wantHash || metadata.Size != manifest.Size || metadata.Object != manifest.Object ||
			len(metadata.ExcludedFields) != 1 || metadata.ExcludedFields[0] != "public.logs.other.po" {
			t.Fatalf("published manifest does not match filtered backup: %+v", metadata)
		}
	}
	if entries, err := os.ReadDir(cfg.StateDir); err != nil || len(entries) != 0 {
		t.Fatalf("state directory was not cleaned: %v, %v", entries, err)
	}
}

func TestDumpPostgresDoesNotPublishFailedSnapshots(t *testing.T) {
	for _, test := range []struct {
		name        string
		input       string
		exitCode    string
		keepWriting bool
	}{
		{
			name:     "malformed JSON cancels pg_dump",
			input:    logsDataSection + "COPY public.logs (id, other) FROM stdin;\n1\tprivate prompt is not JSON\n",
			exitCode: "0", keepWriting: true,
		},
		{
			name:     "truncated COPY",
			input:    logsDataSection + "COPY public.logs (id, other) FROM stdin;\n1\t{}\n",
			exitCode: "0",
		},
		{
			name:     "pg_dump failure",
			input:    "-- pg_dump stopped early\n",
			exitCode: "7",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			installDumpFixture(t, test.input)
			t.Setenv("BACKUP_TEST_DUMP_EXIT_CODE", test.exitCode)
			if test.keepWriting {
				t.Setenv("BACKUP_TEST_DUMP_KEEP_WRITING", "true")
			}
			cfg := testConfig(t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			uploads := 0
			_, err := RunOnce(ctx, cfg, time.Now(), DumpPostgres,
				func(context.Context, string, string, string) error { uploads++; return nil })
			if err == nil {
				t.Fatal("failed backup was accepted")
			}
			if ctx.Err() != nil {
				t.Fatal("pg_dump was not stopped after filtering failed")
			}
			if uploads != 0 {
				t.Fatalf("failed snapshot published %d objects", uploads)
			}
			if strings.Contains(err.Error(), "private prompt") {
				t.Fatal("error includes private log content")
			}
			if entries, err := os.ReadDir(cfg.StateDir); err != nil || len(entries) != 0 {
				t.Fatalf("failed backup left temporary files: %v, %v", entries, err)
			}
		})
	}
}
