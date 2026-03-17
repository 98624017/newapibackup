# Repository Guidelines

## Project Structure & Module Organization

- `.github/workflows/backup-to-r2.yml`: the primary GitHub Actions workflow. It runs scheduled/manual PostgreSQL
  backups, compresses dumps (`*.sql.gz`), and uploads them to Cloudflare R2 via the S3 API (optionally to a
  secondary R2 account/bucket).
- `.spec-workflow/`: templates used for spec/planning workflows; not required for runtime.

Backups are uploaded under `s3://<bucket>/<YYYY>/<MM>/` (primary: `R2_BUCKET_NAME`, secondary:
`R2_2_BUCKET_NAME`) with filenames like `<db>-backup-<YYYYMMDD-HHMMSS>.sql.gz`.

## Build, Test, and Development Commands

This repository is workflow-driven (no app build). Recommended local validation:

- `actionlint`: lint GitHub Actions workflow syntax and common mistakes.
- `act -W .github/workflows/backup-to-r2.yml`: optional local execution of the workflow (provide secrets via `-s`).

For production validation, open a PR and verify the GitHub Actions run completes successfully.

## Coding Style & Naming Conventions

- YAML: 2-space indentation; keep structure consistent (`on` → `jobs` → `steps`); prefer `kebab-case.yml` names.
- Bash in `run:` blocks: quote variables, prefer strict modes (`set -euo pipefail`) when feasible, and keep steps
  small and readable.
- Secrets/env vars: use `UPPER_SNAKE_CASE` (e.g., `DB_BACKUP_CONFIG`, `R2_BUCKET_NAME`, `R2_2_BUCKET_NAME`).

## Testing Guidelines

No unit-test suite is defined. Every change should at least pass `actionlint`, and ideally be exercised with `act`
or a PR run.

## Commit & Pull Request Guidelines

Commit history commonly uses a typed prefix with a colon (e.g., `feat: ...`, `Fix: ...`, `Debug: ...`). Prefer a
consistent Conventional Commits style (`feat:`, `fix:`, `chore:`) and keep messages imperative.

PRs should include: intent, scope, validation notes (link to a workflow run), and any secret/config changes. Attach
redacted logs if useful.

## Security & Configuration Tips

Never commit credentials or database URLs. Configure GitHub Secrets used by the workflow:

- `DB_BACKUP_CONFIG`: JSON array like `[{"name":"prod","url":"postgres://...","targets":"both"}]` (per-item
  `targets` is optional and overrides the default upload target)
- Primary R2: `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, `R2_ACCOUNT_ID`, `R2_BUCKET_NAME`
- Secondary R2 (optional): `R2_2_ACCESS_KEY_ID`, `R2_2_SECRET_ACCESS_KEY`, `R2_2_ACCOUNT_ID`, `R2_2_BUCKET_NAME`
- Upload selector (optional): set `R2_UPLOAD_TARGETS` to `primary`, `secondary`, or `both` (prefer Actions
  Variables; Secrets also supported). If unset, the workflow defaults to `both` when secondary secrets are fully
  configured, otherwise `primary`.

Avoid printing secrets; keep debug output minimal and redact connection strings in logs and PR descriptions.

For local, repeatable setup, copy `config/backup-config.example.yml` to `config/backup-config.local.yml` (gitignored)
and apply it via `python scripts/apply-gh-actions-config.py --config config/backup-config.local.yml` (use
`--dry-run` first).
