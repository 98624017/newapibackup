# Single-DB Go Docker Backup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the old Python/AWS CLI backup worker with a lightweight Go Docker service that backs up one PostgreSQL database to Cloudflare R2.

**Architecture:** The Go binary reads environment variables, runs `pg_dump`, gzip-compresses the dump to a temp file, uploads backup files to R2 through the S3 API, and loops on `BACKUP_INTERVAL_SECONDS`. Docker uses a Go 1.26 builder image and a small Alpine runtime with `postgresql-client` and CA certificates.

**Tech Stack:** Go 1.26, AWS SDK for Go v2 S3 client, Alpine Linux, PostgreSQL client tools, Docker.

---

### Task 1: Go Module And Config

**Files:**
- Create: `go.mod`
- Create: `internal/backup/config.go`
- Create: `internal/backup/config_test.go`

- [ ] **Step 1: Write failing config tests**

Create `internal/backup/config_test.go` with tests for required env vars, defaults, prefix cleanup, invalid interval, and disabled `BACKUP_ON_START`.

- [ ] **Step 2: Run config tests to verify failure**

Run: `timeout 60s go test ./internal/backup -run TestLoadConfig -v`

Expected: FAIL because `LoadConfig` does not exist.

- [ ] **Step 3: Implement config parsing**

Create `internal/backup/config.go` with:

```go
type Config struct {
    DatabaseURL     string
    BackupName      string
    Interval        time.Duration
    BackupOnStart   bool
    R2AccountID     string
    R2AccessKeyID   string
    R2SecretKey     string
    R2BucketName    string
    R2Prefix        string
    StateDir        string
}
```

Implement `LoadConfig(getenv func(string) string) (Config, error)`.

- [ ] **Step 4: Run config tests**

Run: `timeout 60s go test ./internal/backup -run TestLoadConfig -v`

Expected: PASS.

### Task 2: Backup Paths And Manifest

**Files:**
- Create: `internal/backup/manifest.go`
- Create: `internal/backup/manifest_test.go`

- [ ] **Step 1: Write failing manifest tests**

Test object key generation, prefixed keys, empty prefix, and manifest fields.

- [ ] **Step 2: Run manifest tests to verify failure**

Run: `timeout 60s go test ./internal/backup -run 'TestBuild|TestNewManifest' -v`

Expected: FAIL because functions do not exist.

- [ ] **Step 3: Implement manifest helpers**

Implement:

```go
func BuildBackupKey(name string, createdAt time.Time) string
func JoinPrefix(prefix, key string) string
func NewManifest(name string, createdAt time.Time, objectKey string, sha256 string, size int64) Manifest
```

- [ ] **Step 4: Run manifest tests**

Run: `timeout 60s go test ./internal/backup -run 'TestBuild|TestNewManifest' -v`

Expected: PASS.

### Task 3: Dump, Hash, And Upload Orchestration

**Files:**
- Create: `internal/backup/runner.go`
- Create: `internal/backup/runner_test.go`

- [ ] **Step 1: Write failing runner tests**

Use fake dump and upload functions to verify upload order: backup `.sql.gz`, backup `.json`, then `full/latest.json`; verify temp files are cleaned after success and upload failure.

- [ ] **Step 2: Run runner tests to verify failure**

Run: `timeout 60s go test ./internal/backup -run TestRunOnce -v`

Expected: FAIL because `RunOnce` does not exist.

- [ ] **Step 3: Implement runner**

Implement `RunOnce(ctx context.Context, cfg Config, now time.Time, dumper Dumper, uploader Uploader) (Manifest, error)` and production dump/upload adapters.

- [ ] **Step 4: Run runner tests**

Run: `timeout 60s go test ./internal/backup -run TestRunOnce -v`

Expected: PASS.

### Task 4: CLI Loop

**Files:**
- Create: `cmd/backup-worker/main.go`
- Create: `internal/backup/scheduler.go`
- Create: `internal/backup/scheduler_test.go`

- [ ] **Step 1: Write failing scheduler tests**

Test that backup-on-start runs immediately, disabled backup-on-start waits for the first interval, and context cancellation exits.

- [ ] **Step 2: Run scheduler tests to verify failure**

Run: `timeout 60s go test ./internal/backup -run TestRunLoop -v`

Expected: FAIL because `RunLoop` does not exist.

- [ ] **Step 3: Implement scheduler and CLI**

Implement signal-aware CLI that loads config, creates S3 uploader, and calls scheduler.

- [ ] **Step 4: Run scheduler and package tests**

Run: `timeout 60s go test ./...`

Expected: PASS.

### Task 5: Docker And Docs

**Files:**
- Create: `Dockerfile`
- Create: `.dockerignore`
- Modify: `README.md`
- Modify: `.gitignore`

- [ ] **Step 1: Add Dockerfile**

Use `golang:1.26-alpine` builder and `alpine` runtime. Runtime installs `ca-certificates` and `postgresql-client`.

- [ ] **Step 2: Add README deployment instructions**

Document Zeabur environment variables, Docker deployment, backup paths, restore command, and validation.

- [ ] **Step 3: Build Docker image**

Run: `timeout 60s docker build -t newapi-backup-worker:test .`

Expected: PASS, unless Docker daemon is unavailable.

### Task 6: Remove Old Python Path

**Files:**
- Delete: `scripts/zeabur_backup_worker.py`
- Delete: `scripts/worker_backup_lib.py`
- Delete: `scripts/mirror_r2_full.py`
- Delete: `scripts/apply-gh-actions-config.py`
- Delete: `tests/test_zeabur_backup_worker.py`
- Delete: `tests/test_worker_backup_lib.py`
- Delete: `tests/test_mirror_r2_full.py`
- Delete: `requirements-dev.txt`
- Delete: `config/backup-worker-config.example.json`
- Delete: `config/backup-config.example.yml`

- [ ] **Step 1: Remove Python files**

Delete obsolete Python implementation and tests.

- [ ] **Step 2: Search for stale references**

Run: `rg -n "Python|pytest|GitHub Actions|BACKUP_WORKER_CONFIG|DB_BACKUP_CONFIG|AWS CLI|mirror-r2|backup-to-r2|multi-db|多数据库" README.md AGENTS.md .github config scripts tests docs/superpowers/specs/2026-05-07-single-db-go-docker-backup-design.md`

Expected: only historical docs may mention old paths; runtime docs should not.

- [ ] **Step 3: Run final verification**

Run: `timeout 60s go test ./...`

Expected: PASS.
