package backup

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDumpPostgresRestoresWithPostgres(t *testing.T) {
	for _, encoding := range []string{"UTF8", "LATIN1"} {
		t.Run(encoding, func(t *testing.T) {
			testDumpPostgresRestoresWithPostgres(t, encoding)
		})
	}
}

func testDumpPostgresRestoresWithPostgres(t *testing.T, encoding string) {
	t.Helper()
	container := os.Getenv("BACKUP_TEST_POSTGRES_CONTAINER")
	if container == "" {
		t.Skip("set BACKUP_TEST_POSTGRES_CONTAINER to a temporary PostgreSQL container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	label, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{ index .Config.Labels "newapibackup.restore-test" }}`, container).CombinedOutput()
	if err != nil || strings.TrimSpace(string(label)) != "true" {
		t.Fatalf("use a temporary container labeled newapibackup.restore-test=true: %v\n%s", err, label)
	}
	sourceDB := fmt.Sprintf("backup_filter_source_%d", os.Getpid())
	restoreDB := fmt.Sprintf("backup_filter_restore_%d", os.Getpid())
	for _, database := range []string{sourceDB, restoreDB} {
		output, err := exec.CommandContext(ctx, "docker", "exec", container, "createdb", "--username=postgres", "--template=template0", "--locale=C", "--encoding="+encoding, database).CombinedOutput()
		if err != nil {
			t.Fatalf("create fixture database %s: %v\n%s", database, err, output)
		}
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			output, err := exec.CommandContext(cleanupCtx, "docker", "exec", container, "dropdb", "--username=postgres", "--force", database).CombinedOutput()
			if err != nil {
				t.Errorf("drop fixture database %s: %v\n%s", database, err, output)
			}
		})
	}

	text := "中文🧪"
	if encoding == "LATIN1" {
		text = "café"
	}
	expectedOther := fmt.Sprintf(`{"billing":{"tokens":9007199254740993,"price":0.00000013},"unicode":"%s","escaped":"quote\"\\path\tline\nnext","nested":{"po":{"keep":true}}}`, text)
	unchangedOther := fmt.Sprintf(` { "nested": { "po": "keep" }, "text": "literal\\path %s" } `, text)
	const paramOverride = `{"rules":[{"path":"max_tokens","value":1024}],"po":{"enabled":true}}`
	fixtureSQL := fmt.Sprintf(`
CREATE TABLE public.logs (id bigserial PRIMARY KEY, quota bigint NOT NULL, other text, content text NOT NULL);
CREATE TABLE public.channels (id serial PRIMARY KEY, param_override text NOT NULL);
CREATE TABLE public.users (id serial PRIMARY KEY, quota bigint NOT NULL, balance numeric(20,6) NOT NULL);
INSERT INTO public.logs (quota, other, content) VALUES
  (123, ($fixture$%s$fixture$::jsonb || jsonb_build_object('po', jsonb_build_object('audit', repeat('%s', 30000))))::text, E'%s\tline\n\\path'),
  (456, $fixture$%s$fixture$, 'without top-level audit'),
  (789, NULL, 'null other');
INSERT INTO public.channels (param_override) VALUES ($fixture$%s$fixture$);
INSERT INTO public.users (quota, balance) VALUES (2500000, 12345.678901);
`, expectedOther, text, text, unchangedOther, paramOverride)
	restoreFixtureSQL(t, ctx, container, sourceDB, strings.NewReader(fixtureSQL))

	const snapshotSQL = `SELECT jsonb_build_object(
  'logs', (SELECT jsonb_agg(to_jsonb(logs) || jsonb_build_object('other', %s) ORDER BY id) FROM public.logs),
  'channels', (SELECT jsonb_agg(to_jsonb(channels) ORDER BY id) FROM public.channels),
  'users', (SELECT jsonb_agg(to_jsonb(users) ORDER BY id) FROM public.users),
  'columns', (SELECT jsonb_agg(jsonb_build_array(table_name, column_name, data_type, is_nullable, column_default) ORDER BY table_name, ordinal_position)
    FROM information_schema.columns WHERE table_schema = 'public' AND table_name IN ('logs', 'channels', 'users')),
  'sequences', jsonb_build_array(
    (SELECT jsonb_build_array(last_value, is_called) FROM public.logs_id_seq),
    (SELECT jsonb_build_array(last_value, is_called) FROM public.channels_id_seq),
    (SELECT jsonb_build_array(last_value, is_called) FROM public.users_id_seq))
)::text;`
	want := restoreFixtureSQL(t, ctx, container, sourceDB, strings.NewReader(fmt.Sprintf(snapshotSQL, "other::jsonb - 'po'")))

	tmp := t.TempDir()
	wrapper := "#!/bin/sh\nexec docker exec \"$BACKUP_TEST_POSTGRES_CONTAINER\" pg_dump \"$@\"\n"
	if err := os.WriteFile(filepath.Join(tmp, "pg_dump"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	backupPath := filepath.Join(tmp, "backup.sql.gz")
	if err := DumpPostgres(ctx, "postgresql:///"+sourceDB+"?user=postgres", backupPath); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	restoreFixtureSQL(t, ctx, container, restoreDB, reader)

	got := restoreFixtureSQL(t, ctx, container, restoreDB, strings.NewReader(fmt.Sprintf(snapshotSQL, "other::jsonb")))
	if got != want {
		t.Fatalf("restored schema, rows, quotas, JSON, channel settings or sequences differ:\ngot  %s\nwant %s", got, want)
	}
	rawOtherSQL := fmt.Sprintf("SELECT other = $fixture$%s$fixture$ FROM public.logs WHERE id = 2;", unchangedOther)
	if got := restoreFixtureSQL(t, ctx, container, restoreDB, strings.NewReader(rawOtherSQL)); got != "t" {
		t.Fatalf("logs.other without top-level po changed: %s", got)
	}
	if got := restoreFixtureSQL(t, ctx, container, restoreDB, strings.NewReader("SELECT nextval('public.logs_id_seq'), nextval('public.channels_id_seq'), nextval('public.users_id_seq');")); got != "4|2|2" {
		t.Fatalf("restored next IDs = %s, want 4|2|2", got)
	}
	if got := restoreFixtureSQL(t, ctx, container, sourceDB, strings.NewReader("SELECT octet_length(other::jsonb -> 'po' ->> 'audit') > 65536 FROM public.logs WHERE id = 1;")); got != "t" {
		t.Fatalf("source audit missing or not larger than 64 KiB: %s", got)
	}
}

func restoreFixtureSQL(t *testing.T, ctx context.Context, container, database string, input io.Reader) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", "--env=PGCLIENTENCODING=UTF8", container, "psql", "--no-psqlrc", "--username=postgres", "--dbname="+database, "--set=ON_ERROR_STOP=1", "--tuples-only", "--no-align", "--quiet")
	cmd.Stdin = input
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("psql fixture database %s: %v\n%s", database, err, output)
	}
	return strings.TrimSpace(string(output))
}
