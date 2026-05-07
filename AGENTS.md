# Repository Guidelines

## Project Structure & Module Organization

- `cmd/backup-worker/main.go`: container entrypoint. It loads environment variables, creates the R2 client, and runs
  the backup loop.
- `internal/backup/`: core backup package. It contains configuration parsing, scheduling, `pg_dump` execution,
  gzip output, manifest generation, SHA256 hashing, and R2/S3 uploads.
- `Dockerfile`: multi-stage Docker build for Zeabur deployment. The runtime image installs only CA certificates and
  `postgresql-client`.
- `.github/workflows/docker-publish.yml`: builds and pushes the Docker image to GitHub Container Registry on push or
  manual dispatch.
- `README.md`: deployment, environment variable, R2 object path, restore, and validation instructions.
- `docs/superpowers/`: design and implementation planning notes; not required for runtime.
- `.spec-workflow/`: templates used for spec/planning workflows; not required for runtime.

This project intentionally uses one Docker container per database. It does not keep the old Python worker,
AWS CLI upload path, multi-database config, or GitHub Actions database backup workflow.

Backups are uploaded under:

```text
s3://<bucket>/<prefix>/full/YYYY/MM/<backup-name>-backup-YYYYMMDD-HHMMSS.sql.gz
s3://<bucket>/<prefix>/full/YYYY/MM/<backup-name>-backup-YYYYMMDD-HHMMSS.sql.gz.json
s3://<bucket>/<prefix>/full/latest.json
```

## Build, Test, and Development Commands

- `timeout 60s go test ./...`: run all Go tests.
- `timeout 60s docker build -t newapi-backup-worker:test .`: build the deployment image.
- `go test ./internal/backup -run TestLoadConfig -v`: run a focused test while iterating on config parsing.

## Coding Style & Naming Conventions

- Go: use `gofmt`; keep package APIs small and explicit.
- Environment variables: use `UPPER_SNAKE_CASE`, matching README names exactly.
- R2 object keys: keep `full/YYYY/MM/` layout stable because restore and `latest.json` depend on it.
- Comments: add short comments only for complex or easy-to-misread logic.

## Testing Guidelines

Every behavior change should have Go tests. Prefer testing the core package with fake dump/upload functions instead
of touching a real database or R2 bucket. Before handing off changes, run:

```bash
timeout 60s go test ./...
```

When Docker is available, also run:

```bash
timeout 60s docker build -t newapi-backup-worker:test .
```

## Commit & Pull Request Guidelines

Commit history commonly uses a typed prefix with a colon (e.g., `feat: ...`, `fix: ...`, `chore: ...`). Keep messages
imperative and focused.

PRs should include intent, scope, validation notes, and any environment variable changes. Attach redacted logs if
useful.

## Security & Configuration Tips

Never commit credentials or database URLs. Configure these as Zeabur environment variables:

```text
DATABASE_URL
R2_ACCOUNT_ID
R2_ACCESS_KEY_ID
R2_SECRET_ACCESS_KEY
R2_BUCKET_NAME
```

Optional variables:

```text
BACKUP_NAME
BACKUP_INTERVAL_SECONDS
BACKUP_ON_START
R2_PREFIX
BACKUP_STATE_DIR
```

Avoid printing secrets. Logs may include backup object names, sizes, and hashes, but must not include `DATABASE_URL`
or R2 secret values.
