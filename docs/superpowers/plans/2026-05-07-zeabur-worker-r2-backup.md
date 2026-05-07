# Zeabur Worker R2 Backup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a simpler backup pipeline where a Zeabur-side backup-worker runs `pg_dump | gzip`, uploads one compressed full backup to primary R2, and GitHub Actions mirrors verified full backups to secondary R2.

**Architecture:** Add a small Python backup-worker script for server-side execution and a separate GitHub Actions mirror/health workflow. Keep the current `backup-to-r2.yml` in place as a temporary fallback until worker restores are verified. Avoid xdelta, WAL, and cloud-side reconstruction in the first stage.

**Tech Stack:** Python 3 standard library, `pytest`, PostgreSQL client tools, `gzip`, AWS CLI against Cloudflare R2 S3 API, GitHub Actions YAML.

---

## Scope Check

This replaces the previous delta-transfer implementation plan as the recommended first-stage implementation. It does not delete the prior docs, but the new spec supersedes them.

NOT in scope:

- xdelta diff generation
- GitHub Actions full reconstruction
- WAL/PITR
- replacing the existing backup workflow immediately
- Zeabur CLI as a runtime backup dependency

## File Structure

```text
scripts/
  worker_backup_lib.py         # Shared JSON/config/hash/R2 helpers
  zeabur_backup_worker.py      # Server-side pg_dump | gzip backup uploader
  mirror_r2_full.py            # GitHub Actions primary -> secondary mirror and lag check

tests/
  test_worker_backup_lib.py
  test_zeabur_backup_worker.py
  test_mirror_r2_full.py

.github/workflows/
  mirror-r2-full.yml

config/
  backup-worker-config.example.json
```

## Task 1: Add Test Scaffold

**Files:**
- Create: `requirements-dev.txt`
- Create: `tests/test_worker_backup_lib.py`

- [ ] **Step 1: Add test dependency**

Create `requirements-dev.txt`:

```text
pytest==8.3.5
```

- [ ] **Step 2: Add first failing tests**

Create `tests/test_worker_backup_lib.py`:

```python
from __future__ import annotations

import json

import pytest

from scripts.worker_backup_lib import dump_json, load_json, sha256_file


def test_sha256_file_hashes_file_contents(tmp_path):
    path = tmp_path / "data.txt"
    path.write_text("abc", encoding="utf-8")

    assert sha256_file(path) == "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"


def test_json_round_trip_uses_utf8_and_sorted_keys(tmp_path):
    path = tmp_path / "state.json"

    dump_json(path, {"b": 1, "a": "中文"})

    assert json.loads(path.read_text(encoding="utf-8")) == {"a": "中文", "b": 1}
    assert load_json(path) == {"a": "中文", "b": 1}


def test_load_json_rejects_non_object(tmp_path):
    path = tmp_path / "bad.json"
    path.write_text("[]", encoding="utf-8")

    with pytest.raises(ValueError, match="must be a JSON object"):
        load_json(path)
```

- [ ] **Step 3: Verify expected failure**

```bash
timeout 60s python -m pytest tests/test_worker_backup_lib.py -q
```

Expected: FAIL with `ModuleNotFoundError: No module named 'scripts.worker_backup_lib'`.

- [ ] **Step 4: Commit**

```bash
git add requirements-dev.txt tests/test_worker_backup_lib.py
git commit -m "test: add worker backup test scaffold"
```

## Task 2: Shared Worker Backup Library

**Files:**
- Create: `scripts/worker_backup_lib.py`
- Modify: `tests/test_worker_backup_lib.py`

- [ ] **Step 1: Implement shared helpers**

Create `scripts/worker_backup_lib.py`:

```python
#!/usr/bin/env python3
from __future__ import annotations

import hashlib
import json
import os
import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Mapping, Sequence


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def load_json(path: Path) -> dict[str, Any]:
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        raise ValueError(f"{path} must be a JSON object.")
    return data


def dump_json(path: Path, data: Mapping[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, sort_keys=True, indent=2) + "\n", encoding="utf-8")


def load_config_from_env(env_name: str) -> dict[str, Any]:
    raw = os.environ.get(env_name, "")
    if not raw:
        raise ValueError(f"Missing config env var: {env_name}")
    data = json.loads(raw)
    if not isinstance(data, dict):
        raise ValueError(f"{env_name} must be a JSON object.")
    return data


def run_cmd(cmd: Sequence[str], *, env: Mapping[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    merged_env = os.environ.copy()
    if env:
        merged_env.update(env)
    return subprocess.run(list(cmd), check=True, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=merged_env)


@dataclass(frozen=True)
class R2Target:
    name: str
    account_env: str
    access_key_env: str
    secret_key_env: str
    bucket_env: str
    prefix: str

    @property
    def endpoint(self) -> str:
        account_id = os.environ.get(self.account_env, "")
        if not account_id:
            raise ValueError(f"Missing env var: {self.account_env}")
        return f"https://{account_id}.r2.cloudflarestorage.com"

    @property
    def bucket(self) -> str:
        bucket = os.environ.get(self.bucket_env, "")
        if not bucket:
            raise ValueError(f"Missing env var: {self.bucket_env}")
        return bucket

    def object_key(self, relative_key: str) -> str:
        return f"{self.prefix.rstrip('/')}/{relative_key.lstrip('/')}"

    def s3_uri(self, relative_key: str) -> str:
        return f"s3://{self.bucket}/{self.object_key(relative_key)}"

    def aws_env(self) -> dict[str, str]:
        access_key = os.environ.get(self.access_key_env, "")
        secret_key = os.environ.get(self.secret_key_env, "")
        if not access_key:
            raise ValueError(f"Missing env var: {self.access_key_env}")
        if not secret_key:
            raise ValueError(f"Missing env var: {self.secret_key_env}")
        return {
            "AWS_ACCESS_KEY_ID": access_key,
            "AWS_SECRET_ACCESS_KEY": secret_key,
            "AWS_DEFAULT_REGION": "auto",
            "AWS_EC2_METADATA_DISABLED": "true",
        }
```

- [ ] **Step 2: Add R2/config tests**

Append to `tests/test_worker_backup_lib.py`:

```python
from scripts.worker_backup_lib import R2Target, load_config_from_env


def test_load_config_from_env_reads_json(monkeypatch):
    monkeypatch.setenv("BACKUP_CONFIG", '{"backup_databases": [], "r2_targets": {}}')

    assert load_config_from_env("BACKUP_CONFIG") == {"backup_databases": [], "r2_targets": {}}


def test_r2_target_builds_prefixed_s3_uri(monkeypatch):
    monkeypatch.setenv("R2_ACCOUNT_ID", "acct")
    monkeypatch.setenv("R2_BUCKET", "bucket")
    monkeypatch.setenv("R2_ACCESS_KEY_ID", "key")
    monkeypatch.setenv("R2_SECRET_ACCESS_KEY", "secret")
    target = R2Target(
        name="primary",
        account_env="R2_ACCOUNT_ID",
        access_key_env="R2_ACCESS_KEY_ID",
        secret_key_env="R2_SECRET_ACCESS_KEY",
        bucket_env="R2_BUCKET",
        prefix="prod-a/",
    )

    assert target.endpoint == "https://acct.r2.cloudflarestorage.com"
    assert target.s3_uri("full/latest.json") == "s3://bucket/prod-a/full/latest.json"
    assert target.aws_env()["AWS_ACCESS_KEY_ID"] == "key"
```

- [ ] **Step 3: Verify**

```bash
timeout 60s python -m pytest tests/test_worker_backup_lib.py -q
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add scripts/worker_backup_lib.py tests/test_worker_backup_lib.py
git commit -m "feat: add worker backup helpers"
```

## Task 3: Zeabur Backup Worker

**Files:**
- Create: `scripts/zeabur_backup_worker.py`
- Create: `tests/test_zeabur_backup_worker.py`

- [ ] **Step 1: Add worker tests**

Create `tests/test_zeabur_backup_worker.py`:

```python
from __future__ import annotations

import datetime as dt

from scripts.zeabur_backup_worker import build_backup_key, build_manifest, pg_dump_command


def test_build_backup_key_uses_db_and_date_path():
    created_at = dt.datetime(2026, 5, 7, 14, 30, tzinfo=dt.timezone.utc)

    assert build_backup_key("prod-a", created_at) == "full/2026/05/prod-a-backup-20260507-143000.sql.gz"


def test_pg_dump_command_uses_plain_clean_dump():
    cmd = pg_dump_command("postgres://example", "/tmp/out.sql.gz")

    assert cmd == [
        "bash",
        "-o",
        "pipefail",
        "-c",
        'pg_dump "$1" --format=plain --no-owner --no-acl --clean --if-exists | gzip -9 > "$2"',
        "pg-dump-pipe",
        "postgres://example",
        "/tmp/out.sql.gz",
    ]


def test_build_manifest_records_object_hash_and_size():
    created_at = dt.datetime(2026, 5, 7, 14, 30, tzinfo=dt.timezone.utc)
    manifest = build_manifest(
        db_name="prod-a",
        created_at=created_at,
        object_key="prod-a/full/2026/05/prod-a-backup-20260507-143000.sql.gz",
        sha256="abc",
        size=18,
    )

    assert manifest["db"] == "prod-a"
    assert manifest["sha256"] == "abc"
    assert manifest["size"] == 18
    assert manifest["format"] == "plain sql gzip"
```

- [ ] **Step 2: Implement worker script**

Create `scripts/zeabur_backup_worker.py` with:

- `build_backup_key(db_name, created_at)`
- `pg_dump_command(db_url, output_path)`
- `build_manifest(db_name, created_at, object_key, sha256, size)`
- CLI args: `--config-env BACKUP_WORKER_CONFIG`, optional `--db-name`, `--state-root`, `--dry-run`
- Serial backup over configured databases, or one database if `--db-name` is set
- Upload `.sql.gz`, `.sql.gz.json`, and `full/latest.json` to each database primary target
- Delete local temp files after successful upload

- [ ] **Step 3: Add upload behavior tests**

Add tests that monkeypatch `run_cmd` and upload helpers so no real database or R2 access happens. Assert:

- dry-run does not upload
- selected `--db-name` only backs up that database
- upload order is `.sql.gz`, manifest JSON, then `latest.json`

- [ ] **Step 4: Verify**

```bash
timeout 60s python -m pytest tests/test_zeabur_backup_worker.py tests/test_worker_backup_lib.py -q
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add scripts/zeabur_backup_worker.py tests/test_zeabur_backup_worker.py
git commit -m "feat: add zeabur backup worker"
```

## Task 4: Secondary Mirror Workflow

**Files:**
- Create: `scripts/mirror_r2_full.py`
- Create: `tests/test_mirror_r2_full.py`
- Create: `.github/workflows/mirror-r2-full.yml`

- [ ] **Step 1: Add mirror tests**

Create `tests/test_mirror_r2_full.py`:

```python
from __future__ import annotations

from scripts.mirror_r2_full import should_mirror_key


def test_should_mirror_full_backup_and_metadata():
    assert should_mirror_key("prod-a/full/2026/05/prod-a-backup-20260507-143000.sql.gz") is True
    assert should_mirror_key("prod-a/full/2026/05/prod-a-backup-20260507-143000.sql.gz.json") is True
    assert should_mirror_key("prod-a/full/latest.json") is True


def test_should_not_mirror_non_full_objects():
    assert should_mirror_key("prod-a/tmp/file.sql.gz") is False
    assert should_mirror_key("prod-a/staging/run/manifest.json") is False
```

- [ ] **Step 2: Implement mirror script**

Create `scripts/mirror_r2_full.py` with:

- `should_mirror_key(key)`
- CLI args: `--config-env BACKUP_WORKER_CONFIG`, `--max-lag-hours 6`
- read config JSON
- list primary keys under each database prefix
- mirror only `full/*.sql.gz`, `full/*.sql.gz.json`, and `full/latest.json`
- skip secondary object if size matches
- fail if primary `latest.json` is older than max lag

- [ ] **Step 3: Add workflow**

Create `.github/workflows/mirror-r2-full.yml`:

```yaml
name: Mirror R2 Full Backups

on:
  schedule:
    - cron: '0 */4 * * *'
  workflow_dispatch:

concurrency:
  group: mirror-r2-full
  cancel-in-progress: false

jobs:
  mirror:
    runs-on: ubuntu-latest
    env:
      BACKUP_WORKER_CONFIG: ${{ secrets.BACKUP_WORKER_CONFIG }}
      AWS_DEFAULT_REGION: auto
      AWS_EC2_METADATA_DISABLED: "true"
    steps:
      - name: Checkout code
        uses: actions/checkout@v4

      - name: Mirror primary R2 full backups to secondary R2
        env:
          R2_PRIMARY_ACCOUNT_ID: ${{ secrets.R2_ACCOUNT_ID }}
          R2_PRIMARY_ACCESS_KEY_ID: ${{ secrets.R2_ACCESS_KEY_ID }}
          R2_PRIMARY_SECRET_ACCESS_KEY: ${{ secrets.R2_SECRET_ACCESS_KEY }}
          R2_PRIMARY_BUCKET_NAME: ${{ secrets.R2_BUCKET_NAME }}
          R2_SECONDARY_ACCOUNT_ID: ${{ secrets.R2_2_ACCOUNT_ID }}
          R2_SECONDARY_ACCESS_KEY_ID: ${{ secrets.R2_2_ACCESS_KEY_ID }}
          R2_SECONDARY_SECRET_ACCESS_KEY: ${{ secrets.R2_2_SECRET_ACCESS_KEY }}
          R2_SECONDARY_BUCKET_NAME: ${{ secrets.R2_2_BUCKET_NAME }}
        run: |
          set -euo pipefail
          python scripts/mirror_r2_full.py --config-env BACKUP_WORKER_CONFIG --max-lag-hours 6
```

- [ ] **Step 4: Verify**

```bash
timeout 60s python -m pytest tests/test_mirror_r2_full.py tests/test_worker_backup_lib.py -q
actionlint .github/workflows/mirror-r2-full.yml
```

Expected: PASS. If `actionlint` is unavailable, record the reason.

- [ ] **Step 5: Commit**

```bash
git add scripts/mirror_r2_full.py tests/test_mirror_r2_full.py .github/workflows/mirror-r2-full.yml
git commit -m "feat: mirror worker backups to secondary r2"
```

## Task 5: Config Example and Documentation

**Files:**
- Create: `config/backup-worker-config.example.json`
- Create or modify: `README.md`
- Modify: `AGENTS.md` if workflow commands materially change

- [ ] **Step 1: Add config example**

Create `config/backup-worker-config.example.json`:

```json
{
  "backup_databases": [
    {
      "name": "prod-a",
      "url_env": "PROD_A_DATABASE_URL",
      "primary_target": "r2-primary-prod-a",
      "mirror_targets": ["r2-secondary-prod-a"],
      "schedule_offset_minutes": 0
    },
    {
      "name": "prod-b",
      "url_env": "PROD_B_DATABASE_URL",
      "primary_target": "r2-primary-prod-b",
      "mirror_targets": [],
      "schedule_offset_minutes": 15
    }
  ],
  "r2_targets": {
    "r2-primary-prod-a": {
      "account_env": "R2_PRIMARY_ACCOUNT_ID",
      "access_key_env": "R2_PRIMARY_ACCESS_KEY_ID",
      "secret_key_env": "R2_PRIMARY_SECRET_ACCESS_KEY",
      "bucket_env": "R2_PRIMARY_BUCKET_NAME",
      "prefix": "prod-a/"
    },
    "r2-secondary-prod-a": {
      "account_env": "R2_SECONDARY_ACCOUNT_ID",
      "access_key_env": "R2_SECONDARY_ACCESS_KEY_ID",
      "secret_key_env": "R2_SECONDARY_SECRET_ACCESS_KEY",
      "bucket_env": "R2_SECONDARY_BUCKET_NAME",
      "prefix": "prod-a/"
    },
    "r2-primary-prod-b": {
      "account_env": "R2_PRIMARY_ACCOUNT_ID",
      "access_key_env": "R2_PRIMARY_ACCESS_KEY_ID",
      "secret_key_env": "R2_PRIMARY_SECRET_ACCESS_KEY",
      "bucket_env": "R2_PRIMARY_BUCKET_NAME",
      "prefix": "prod-b/"
    }
  }
}
```

- [ ] **Step 2: Document worker command**

Add README section:

```markdown
## Zeabur worker backups

The recommended high-frequency path runs `pg_dump | gzip` inside Zeabur or the same server as PostgreSQL, then uploads one compressed `.sql.gz` file to primary R2.

```bash
export BACKUP_WORKER_CONFIG="$(cat config/backup-worker-config.example.json)"
export PROD_A_DATABASE_URL="postgres://user:password@postgres.internal:5432/prod_a"
python scripts/zeabur_backup_worker.py --config-env BACKUP_WORKER_CONFIG --db-name prod-a --state-root /data/backup-worker
```

Run this command from a Zeabur cron/scheduled service or a small backup-worker service. Keep the legacy GitHub Actions backup enabled until at least two worker backups have been restored successfully.
```

- [ ] **Step 3: Document restore command**

Add:

```markdown
Restore:

```bash
aws s3 cp "s3://<bucket>/<db>/full/YYYY/MM/<backup>.sql.gz" .
gzip -t "<backup>.sql.gz"
gzip -dc "<backup>.sql.gz" | psql "$RESTORE_URL"
```
```

- [ ] **Step 4: Verify**

```bash
timeout 60s python -m pytest tests -q
actionlint .github/workflows/backup-to-r2.yml .github/workflows/mirror-r2-full.yml
```

Expected: PASS. If `actionlint` is unavailable, record the reason.

- [ ] **Step 5: Commit**

```bash
git add config/backup-worker-config.example.json README.md AGENTS.md
git commit -m "docs: document zeabur worker backups"
```

## Final Verification

```bash
timeout 60s python -m pytest tests -q
actionlint .github/workflows/backup-to-r2.yml .github/workflows/mirror-r2-full.yml
```

Expected:

- Python tests pass.
- Workflow lint passes or missing `actionlint` is explicitly reported.
- No xdelta/delta-transfer scripts are created by this implementation.
