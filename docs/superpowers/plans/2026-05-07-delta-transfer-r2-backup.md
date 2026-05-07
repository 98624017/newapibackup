# Delta Transfer R2 Backup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a first-stage backup pipeline where the server uploads only delta/full staging objects, GitHub Actions rebuilds and publishes verified full backups to primary R2, and a separate workflow mirrors verified full backups to secondary R2.

**Architecture:** Keep the current repository workflow-driven style, but move reusable backup behavior into small Python scripts. Server-side backup generation runs from `scripts/create_delta_backup.py`; GitHub Actions publication runs from `scripts/publish_delta_backup.py`; secondary mirroring runs from `scripts/mirror_r2_full.py`. All scripts share config parsing, object naming, hash validation, and command execution helpers from `scripts/delta_backup_lib.py`.

**Tech Stack:** Python 3 standard library, `pytest`, PostgreSQL client tools (`pg_dump`, `pg_restore`), `xdelta3`, `zstd`, AWS CLI against Cloudflare R2 S3 API, GitHub Actions YAML.

---

## Scope Check

This plan intentionally does not implement WAL, WAL-G, `pg_receivewal`, PITR, or a new Postgres image. It also does not replace the current legacy workflow in one big cutover. The first implementation creates the delta-transfer pipeline alongside the existing workflow so it can be tested, then the legacy workflow can be disabled after a verified run.

The plan touches fewer than 8 product files if tests are excluded:

- `scripts/delta_backup_lib.py`
- `scripts/create_delta_backup.py`
- `scripts/publish_delta_backup.py`
- `scripts/mirror_r2_full.py`
- `.github/workflows/delta-backup-publish.yml`
- `.github/workflows/delta-backup-mirror.yml`
- `config/backup-config.example.yml`
- `README.md`

## File Structure

```text
scripts/
  delta_backup_lib.py          # Shared config, hash, R2 CLI, command helpers, manifest/latest schemas
  create_delta_backup.py       # Server/Zeabur-side pg_dump + xdelta/full staging uploader
  publish_delta_backup.py      # GitHub Actions-side staging validator + full publisher
  mirror_r2_full.py            # GitHub Actions-side primary full/ mirror to secondary R2

tests/
  test_delta_backup_lib.py
  test_create_delta_backup.py
  test_publish_delta_backup.py
  test_mirror_r2_full.py

.github/workflows/
  delta-backup-publish.yml
  delta-backup-mirror.yml
```

## Data Flow

```text
Server scheduled run
    |
    v
create_delta_backup.py
    |
    |-- no local base and no R2 latest -> full staging
    |-- local/R2 base exists -> delta staging, unless delta > threshold
    v
primary R2 staging/<run-id>/
    |
    v
delta-backup-publish.yml
    |
    v
publish_delta_backup.py
    |
    |-- validate manifest and hashes
    |-- rebuild or accept full dump
    |-- pg_restore --list
    |-- latest still matches base_object
    v
primary R2 full/*.dump.zst + latest.json
    |
    v
delta-backup-mirror.yml
    |
    v
secondary R2 full/*.dump.zst + latest.json
```

## Task 1: Add Minimal Python Test Infrastructure

**Files:**
- Create: `requirements-dev.txt`
- Create: `tests/test_delta_backup_lib.py`

- [ ] **Step 1: Add development test dependency**

Create `requirements-dev.txt`:

```text
pytest==8.3.5
PyYAML==6.0.2
```

- [ ] **Step 2: Write the first failing smoke tests**

Create `tests/test_delta_backup_lib.py`:

```python
from __future__ import annotations

import json

import pytest

from scripts.delta_backup_lib import sha256_file, load_json, dump_json


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

- [ ] **Step 3: Run tests and verify they fail**

Run:

```bash
timeout 60s python -m pytest tests/test_delta_backup_lib.py -q
```

Expected: FAIL with `ModuleNotFoundError: No module named 'scripts.delta_backup_lib'`.

- [ ] **Step 4: Commit**

```bash
git add requirements-dev.txt tests/test_delta_backup_lib.py
git commit -m "test: add delta backup test scaffold"
```

## Task 2: Implement Shared Delta Backup Library

**Files:**
- Create: `scripts/delta_backup_lib.py`
- Modify: `tests/test_delta_backup_lib.py`

- [ ] **Step 1: Implement shared helpers**

Create `scripts/delta_backup_lib.py`:

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


SCHEMA_VERSION = 1


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


def require_keys(data: Mapping[str, Any], keys: Sequence[str], *, label: str) -> None:
    missing = [key for key in keys if data.get(key) in (None, "")]
    if missing:
        raise ValueError(f"{label} missing required keys: {', '.join(missing)}")


def run_cmd(cmd: Sequence[str], *, env: Mapping[str, str] | None = None, cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
    merged_env = os.environ.copy()
    if env:
        merged_env.update(env)
    return subprocess.run(
        list(cmd),
        check=True,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=merged_env,
        cwd=str(cwd) if cwd else None,
    )


@dataclass(frozen=True)
class R2Target:
    name: str
    account_id_env: str
    access_key_env: str
    secret_key_env: str
    bucket_env: str
    prefix: str

    @property
    def endpoint(self) -> str:
        account_id = os.environ.get(self.account_id_env, "")
        if not account_id:
            raise ValueError(f"Missing env var: {self.account_id_env}")
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


def aws_cp_upload(local_path: Path, target: R2Target, relative_key: str) -> None:
    run_cmd(
        ["aws", "s3", "cp", str(local_path), target.s3_uri(relative_key), "--endpoint-url", target.endpoint],
        env=target.aws_env(),
    )


def aws_cp_download(target: R2Target, relative_key: str, local_path: Path) -> None:
    local_path.parent.mkdir(parents=True, exist_ok=True)
    run_cmd(
        ["aws", "s3", "cp", target.s3_uri(relative_key), str(local_path), "--endpoint-url", target.endpoint],
        env=target.aws_env(),
    )
```

- [ ] **Step 2: Add config and path tests**

Append to `tests/test_delta_backup_lib.py`:

```python
import os

from scripts.delta_backup_lib import R2Target, require_keys


def test_require_keys_reports_missing_values():
    with pytest.raises(ValueError, match="config missing required keys: url, target"):
        require_keys({"name": "prod", "url": ""}, ["url", "target"], label="config")


def test_r2_target_builds_prefixed_s3_uri(monkeypatch):
    monkeypatch.setenv("R2_ACCOUNT_ID", "acct")
    monkeypatch.setenv("R2_BUCKET", "bucket")
    monkeypatch.setenv("R2_ACCESS_KEY_ID", "key")
    monkeypatch.setenv("R2_SECRET_ACCESS_KEY", "secret")
    target = R2Target(
        name="primary",
        account_id_env="R2_ACCOUNT_ID",
        access_key_env="R2_ACCESS_KEY_ID",
        secret_key_env="R2_SECRET_ACCESS_KEY",
        bucket_env="R2_BUCKET",
        prefix="prod-a/",
    )

    assert target.endpoint == "https://acct.r2.cloudflarestorage.com"
    assert target.s3_uri("full/latest.json") == "s3://bucket/prod-a/full/latest.json"
    assert target.aws_env()["AWS_ACCESS_KEY_ID"] == "key"
```

- [ ] **Step 3: Run tests**

Run:

```bash
timeout 60s python -m pytest tests/test_delta_backup_lib.py -q
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add scripts/delta_backup_lib.py tests/test_delta_backup_lib.py
git commit -m "feat: add delta backup shared helpers"
```

## Task 3: Implement Server-Side Staging Creator

**Files:**
- Create: `scripts/create_delta_backup.py`
- Create: `tests/test_create_delta_backup.py`

- [ ] **Step 1: Write state-machine tests**

Create `tests/test_create_delta_backup.py`:

```python
from __future__ import annotations

from pathlib import Path

from scripts.create_delta_backup import should_skip_for_inflight, choose_upload_mode


def test_should_skip_when_pending_manifest_exists(tmp_path):
    state_dir = tmp_path / "prod-a"
    state_dir.mkdir()
    (state_dir / "pending_manifest.json").write_text("{}", encoding="utf-8")

    assert should_skip_for_inflight(state_dir) is True


def test_should_not_skip_without_pending_manifest(tmp_path):
    assert should_skip_for_inflight(tmp_path / "prod-a") is False


def test_choose_upload_mode_uses_full_when_no_base(tmp_path):
    pending = tmp_path / "pending.dump"
    pending.write_bytes(b"abc")

    assert choose_upload_mode(base_dump=None, pending_dump=pending, delta_zst=None, max_delta_ratio=0.70) == "full"


def test_choose_upload_mode_uses_delta_when_delta_is_small(tmp_path):
    base = tmp_path / "base.dump"
    pending = tmp_path / "pending.dump"
    delta = tmp_path / "delta.xdelta.zst"
    base.write_bytes(b"a" * 100)
    pending.write_bytes(b"b" * 100)
    delta.write_bytes(b"c" * 50)

    assert choose_upload_mode(base_dump=base, pending_dump=pending, delta_zst=delta, max_delta_ratio=0.70) == "delta"


def test_choose_upload_mode_falls_back_to_full_when_delta_is_large(tmp_path):
    base = tmp_path / "base.dump"
    pending = tmp_path / "pending.dump"
    delta = tmp_path / "delta.xdelta.zst"
    base.write_bytes(b"a" * 100)
    pending.write_bytes(b"b" * 100)
    delta.write_bytes(b"c" * 71)

    assert choose_upload_mode(base_dump=base, pending_dump=pending, delta_zst=delta, max_delta_ratio=0.70) == "full"
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
timeout 60s python -m pytest tests/test_create_delta_backup.py -q
```

Expected: FAIL with `ModuleNotFoundError` or missing functions.

- [ ] **Step 3: Implement server script helpers and CLI skeleton**

Create `scripts/create_delta_backup.py`:

```python
#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime as dt
import os
from pathlib import Path

try:
    from scripts.delta_backup_lib import dump_json, run_cmd, sha256_file
except ModuleNotFoundError:  # Allows `python scripts/create_delta_backup.py`.
    from delta_backup_lib import dump_json, run_cmd, sha256_file


def should_skip_for_inflight(state_dir: Path) -> bool:
    return (state_dir / "pending_manifest.json").exists()


def choose_upload_mode(*, base_dump: Path | None, pending_dump: Path, delta_zst: Path | None, max_delta_ratio: float) -> str:
    if base_dump is None or not base_dump.exists() or delta_zst is None or not delta_zst.exists():
        return "full"
    return "delta" if delta_zst.stat().st_size <= int(pending_dump.stat().st_size * max_delta_ratio) else "full"


def build_run_id(db_name: str, now: dt.datetime | None = None) -> str:
    now = now or dt.datetime.now(dt.timezone.utc)
    return f"{db_name}-{now.strftime('%Y%m%d-%H%M%S')}"


def main() -> int:
    parser = argparse.ArgumentParser(description="Create delta/full staging backup objects for R2 publication.")
    parser.add_argument("--db-name", required=True)
    parser.add_argument("--db-url-env", required=True)
    parser.add_argument("--state-root", default=os.environ.get("DELTA_BACKUP_STATE_ROOT", "./state"))
    parser.add_argument("--max-delta-ratio", type=float, default=0.70)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    db_url = os.environ.get(args.db_url_env, "")
    if not db_url:
        raise SystemExit(f"Missing database URL env var: {args.db_url_env}")

    state_dir = Path(args.state_root) / args.db_name
    state_dir.mkdir(parents=True, exist_ok=True)
    if should_skip_for_inflight(state_dir):
        print(f"Skipping {args.db_name}: pending staging publication already in flight.")
        return 0

    pending_dump = state_dir / "pending.dump"
    base_dump = state_dir / "base.dump"
    run_id = build_run_id(args.db_name)
    run_cmd(["pg_dump", "-Fc", "-Z0", "--no-owner", "--no-acl", "--file", str(pending_dump), db_url])

    delta_zst = state_dir / "tmp" / f"{run_id}.xdelta.zst"
    if base_dump.exists():
        delta_zst.parent.mkdir(parents=True, exist_ok=True)
        delta_raw = delta_zst.with_suffix("")
        run_cmd(["xdelta3", "-e", "-s", str(base_dump), str(pending_dump), str(delta_raw)])
        run_cmd(["zstd", "-f", "-T0", str(delta_raw), "-o", str(delta_zst)])

    mode = choose_upload_mode(
        base_dump=base_dump if base_dump.exists() else None,
        pending_dump=pending_dump,
        delta_zst=delta_zst if delta_zst.exists() else None,
        max_delta_ratio=args.max_delta_ratio,
    )
    manifest = {
        "schema_version": 1,
        "db": args.db_name,
        "run_id": run_id,
        "mode": mode,
        "result_dump_sha256": sha256_file(pending_dump),
        "result_size": pending_dump.stat().st_size,
        "dump_format": "pg_dump custom -Z0",
    }
    dump_json(state_dir / "pending_manifest.json", manifest)
    print(f"Prepared {mode} staging for {args.db_name}: {run_id}")
    if args.dry_run:
        print("Dry run: upload step intentionally skipped.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

- [ ] **Step 4: Run tests**

Run:

```bash
timeout 60s python -m pytest tests/test_create_delta_backup.py tests/test_delta_backup_lib.py -q
```

Expected: PASS.

- [ ] **Step 5: Extend script to upload staging objects**

Modify `scripts/create_delta_backup.py` to:

- read a target config JSON file passed by `--config`
- resolve the database entry by `--db-name`
- upload `manifest.json` plus either `delta.xdelta.zst` or `full.dump.zst`
- write `pending_manifest.json` only after all uploads succeed

The upload branch must use `aws_cp_upload()` from `delta_backup_lib.py`, and the manifest must contain the exact fields from the spec: `base_archive_sha256`, `base_dump_sha256`, `delta_archive_sha256`, `full_archive_sha256`, `result_dump_sha256`, and `result_size` as applicable.

- [ ] **Step 6: Add upload unit tests with monkeypatched upload helper**

Append tests that monkeypatch `aws_cp_upload()` and `run_cmd()` so no real database or R2 access happens:

```python
def test_successful_upload_writes_pending_manifest_after_uploads(tmp_path, monkeypatch):
    calls = []

    def fake_upload(local_path, target, relative_key):
        calls.append(relative_key)

    monkeypatch.setattr("scripts.create_delta_backup.aws_cp_upload", fake_upload)

    manifest_path = tmp_path / "prod-a" / "pending_manifest.json"
    data_path = tmp_path / "prod-a" / "pending.dump"
    data_path.parent.mkdir(parents=True)
    data_path.write_bytes(b"dump")

    from scripts.create_delta_backup import mark_pending_after_uploads

    mark_pending_after_uploads(
        state_dir=tmp_path / "prod-a",
        manifest={"db": "prod-a", "mode": "full", "result_dump_sha256": "hash"},
        uploaded_keys=["prod-a/staging/run-1/full.dump.zst", "prod-a/staging/run-1/manifest.json"],
    )

    assert manifest_path.exists()
    assert "prod-a/staging/run-1/full.dump.zst" in manifest_path.read_text(encoding="utf-8")
```

- [ ] **Step 7: Run tests and commit**

Run:

```bash
timeout 60s python -m pytest tests/test_create_delta_backup.py tests/test_delta_backup_lib.py -q
```

Expected: PASS.

Commit:

```bash
git add scripts/create_delta_backup.py tests/test_create_delta_backup.py
git commit -m "feat: create delta backup staging script"
```

## Task 4: Implement GitHub Actions Publisher

**Files:**
- Create: `scripts/publish_delta_backup.py`
- Create: `tests/test_publish_delta_backup.py`
- Create: `.github/workflows/delta-backup-publish.yml`

- [ ] **Step 1: Write publisher tests**

Create `tests/test_publish_delta_backup.py`:

```python
from __future__ import annotations

import pytest

from scripts.publish_delta_backup import latest_matches_manifest_base, validate_manifest_mode


def test_validate_manifest_mode_accepts_delta_and_full():
    validate_manifest_mode({"mode": "delta"})
    validate_manifest_mode({"mode": "full"})


def test_validate_manifest_mode_rejects_other_values():
    with pytest.raises(ValueError, match="mode must be delta or full"):
        validate_manifest_mode({"mode": "partial"})


def test_latest_matches_manifest_base_true_for_matching_base_object():
    latest = {"object": "prod-a/full/base.dump.zst"}
    manifest = {"base_object": "prod-a/full/base.dump.zst"}

    assert latest_matches_manifest_base(latest, manifest) is True


def test_latest_matches_manifest_base_false_for_mismatch():
    latest = {"object": "prod-a/full/newer.dump.zst"}
    manifest = {"base_object": "prod-a/full/base.dump.zst"}

    assert latest_matches_manifest_base(latest, manifest) is False
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
timeout 60s python -m pytest tests/test_publish_delta_backup.py -q
```

Expected: FAIL with missing module or functions.

- [ ] **Step 3: Implement publisher validation and CLI**

Create `scripts/publish_delta_backup.py` with these required functions:

```python
#!/usr/bin/env python3
from __future__ import annotations

import argparse
from pathlib import Path
from typing import Mapping, Any

try:
    from scripts.delta_backup_lib import aws_cp_download, aws_cp_upload, dump_json, load_json, require_keys, run_cmd, sha256_file
except ModuleNotFoundError:  # Allows `python scripts/publish_delta_backup.py`.
    from delta_backup_lib import aws_cp_download, aws_cp_upload, dump_json, load_json, require_keys, run_cmd, sha256_file


def validate_manifest_mode(manifest: Mapping[str, Any]) -> None:
    if manifest.get("mode") not in {"delta", "full"}:
        raise ValueError("manifest mode must be delta or full")


def latest_matches_manifest_base(latest: Mapping[str, Any], manifest: Mapping[str, Any]) -> bool:
    return latest.get("object") == manifest.get("base_object")


def assert_hash(path: Path, expected: str, label: str) -> None:
    actual = sha256_file(path)
    if actual != expected:
        raise ValueError(f"{label} sha256 mismatch: expected {expected}, got {actual}")
```

The CLI must:

- accept `--config`, `--db-name`, `--staging-run-id`, and `--work-dir`
- download `manifest.json`
- validate delta/full mode
- for delta mode, download and validate base archive, base dump, and delta archive
- run `xdelta3 -d -s base.dump delta.xdelta rebuilt.dump`
- for full mode, download and validate `full.dump.zst`
- validate `result_dump_sha256`, `result_size`, and `pg_restore --list`
- before updating `latest.json`, confirm current latest still matches `base_object` for delta mode
- upload the final `.dump.zst`, `latest.json`, and a log JSON object

- [ ] **Step 4: Add workflow**

Create `.github/workflows/delta-backup-publish.yml`:

```yaml
name: Delta Backup Publish

on:
  schedule:
    - cron: '*/15 * * * *'
  workflow_dispatch:

concurrency:
  group: delta-backup-publish
  cancel-in-progress: false

jobs:
  publish:
    runs-on: ubuntu-latest
    env:
      DELTA_BACKUP_CONFIG: ${{ secrets.DELTA_BACKUP_CONFIG }}
      AWS_DEFAULT_REGION: auto
      AWS_EC2_METADATA_DISABLED: "true"
    steps:
      - name: Checkout code
        uses: actions/checkout@v4

      - name: Install tools
        run: |
          set -euo pipefail
          sudo apt-get update
          sudo apt-get install -y postgresql-client xdelta3 zstd

      - name: Publish staged backups
        env:
          R2_PRIMARY_ACCOUNT_ID: ${{ secrets.R2_ACCOUNT_ID }}
          R2_PRIMARY_ACCESS_KEY_ID: ${{ secrets.R2_ACCESS_KEY_ID }}
          R2_PRIMARY_SECRET_ACCESS_KEY: ${{ secrets.R2_SECRET_ACCESS_KEY }}
          R2_PRIMARY_BUCKET_NAME: ${{ secrets.R2_BUCKET_NAME }}
        run: |
          set -euo pipefail
          python scripts/publish_delta_backup.py --config-env DELTA_BACKUP_CONFIG --work-dir "$RUNNER_TEMP/delta-publish"
```

- [ ] **Step 5: Run tests and actionlint**

Run:

```bash
timeout 60s python -m pytest tests/test_publish_delta_backup.py tests/test_delta_backup_lib.py -q
actionlint .github/workflows/delta-backup-publish.yml
```

Expected: PASS. If `actionlint` is not installed, install it locally or record that workflow syntax was not locally linted.

- [ ] **Step 6: Commit**

```bash
git add scripts/publish_delta_backup.py tests/test_publish_delta_backup.py .github/workflows/delta-backup-publish.yml
git commit -m "feat: publish verified delta backups"
```

## Task 5: Implement Secondary Full Mirror

**Files:**
- Create: `scripts/mirror_r2_full.py`
- Create: `tests/test_mirror_r2_full.py`
- Create: `.github/workflows/delta-backup-mirror.yml`

- [ ] **Step 1: Write mirror tests**

Create `tests/test_mirror_r2_full.py`:

```python
from __future__ import annotations

from scripts.mirror_r2_full import should_mirror_key


def test_should_mirror_full_dump_and_latest():
    assert should_mirror_key("prod-a/full/prod-a-backup-20260507-143000.dump.zst") is True
    assert should_mirror_key("prod-a/full/latest.json") is True


def test_should_not_mirror_staging():
    assert should_mirror_key("prod-a/staging/run-1/manifest.json") is False
    assert should_mirror_key("prod-a/staging/run-1/full.dump.zst") is False
```

- [ ] **Step 2: Implement mirror script**

Create `scripts/mirror_r2_full.py`:

```python
#!/usr/bin/env python3
from __future__ import annotations

import argparse


def should_mirror_key(key: str) -> bool:
    return "/full/" in key and (key.endswith(".dump.zst") or key.endswith("/latest.json"))


def main() -> int:
    parser = argparse.ArgumentParser(description="Mirror verified full backups from primary R2 to secondary R2.")
    parser.add_argument("--config-env", default="DELTA_BACKUP_CONFIG")
    parser.add_argument("--max-lag-hours", type=int, default=6)
    args = parser.parse_args()
    print(f"Mirroring full backup objects from config env {args.config_env}; max lag {args.max_lag_hours}h")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

Then extend it to:

- list primary objects under each database prefix
- filter with `should_mirror_key`
- skip secondary objects that already exist with matching size
- copy by downloading to runner temp and uploading to secondary R2
- fail if `latest.json` is older than `--max-lag-hours`

- [ ] **Step 3: Add workflow**

Create `.github/workflows/delta-backup-mirror.yml`:

```yaml
name: Delta Backup Mirror

on:
  schedule:
    - cron: '0 */4 * * *'
  workflow_dispatch:

concurrency:
  group: delta-backup-mirror
  cancel-in-progress: false

jobs:
  mirror:
    runs-on: ubuntu-latest
    env:
      DELTA_BACKUP_CONFIG: ${{ secrets.DELTA_BACKUP_CONFIG }}
      AWS_DEFAULT_REGION: auto
      AWS_EC2_METADATA_DISABLED: "true"
    steps:
      - name: Checkout code
        uses: actions/checkout@v4

      - name: Mirror verified full backups
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
          python scripts/mirror_r2_full.py --config-env DELTA_BACKUP_CONFIG --max-lag-hours 6
```

- [ ] **Step 4: Run tests and actionlint**

Run:

```bash
timeout 60s python -m pytest tests/test_mirror_r2_full.py -q
actionlint .github/workflows/delta-backup-mirror.yml
```

Expected: PASS. If `actionlint` is unavailable, record the reason.

- [ ] **Step 5: Commit**

```bash
git add scripts/mirror_r2_full.py tests/test_mirror_r2_full.py .github/workflows/delta-backup-mirror.yml
git commit -m "feat: mirror verified backups to secondary r2"
```

## Task 6: Update Config Example and Configuration Validation

**Files:**
- Modify: `config/backup-config.example.yml`
- Modify: `scripts/apply-gh-actions-config.py`
- Create or modify: `tests/test_apply_gh_actions_config.py`

- [ ] **Step 1: Add config validation tests**

Create `tests/test_apply_gh_actions_config.py` with tests for:

```python
from __future__ import annotations

import pytest

from scripts.apply_gh_actions_config import _validate_delta_backup_config


def test_validate_delta_backup_config_accepts_primary_and_mirror_targets():
    config = {
        "backup_databases": [
            {
                "name": "prod-a",
                "url_secret": "PROD_A_DATABASE_URL",
                "primary_target": "r2-primary-prod-a",
                "mirror_targets": ["r2-secondary-prod-a"],
                "delta": {"enabled": True, "max_delta_ratio": 0.70},
            }
        ],
        "r2_targets": {
            "r2-primary-prod-a": {
                "account_secret": "R2_PRIMARY_ACCOUNT_ID",
                "access_key_secret": "R2_PRIMARY_ACCESS_KEY_ID",
                "secret_key_secret": "R2_PRIMARY_SECRET_ACCESS_KEY",
                "bucket_secret": "R2_PRIMARY_BUCKET_NAME",
                "prefix": "prod-a/",
            },
            "r2-secondary-prod-a": {
                "account_secret": "R2_SECONDARY_ACCOUNT_ID",
                "access_key_secret": "R2_SECONDARY_ACCESS_KEY_ID",
                "secret_key_secret": "R2_SECONDARY_SECRET_ACCESS_KEY",
                "bucket_secret": "R2_SECONDARY_BUCKET_NAME",
                "prefix": "prod-a/",
            },
        },
    }

    assert _validate_delta_backup_config(config) == config


def test_validate_delta_backup_config_rejects_unknown_target():
    config = {
        "backup_databases": [
            {"name": "prod-a", "url_secret": "PROD_A_DATABASE_URL", "primary_target": "missing", "mirror_targets": []}
        ],
        "r2_targets": {},
    }

    with pytest.raises(ValueError, match="unknown primary_target"):
        _validate_delta_backup_config(config)
```

- [ ] **Step 2: Implement validation**

Modify `scripts/apply-gh-actions-config.py` to add `_validate_delta_backup_config(config: Any) -> dict[str, Any]`. It must verify:

- top-level object has `backup_databases` list and `r2_targets` object
- each database has non-empty `name`, `url_secret`, `primary_target`
- `primary_target` exists in `r2_targets`
- every `mirror_targets` entry exists in `r2_targets`
- `delta.max_delta_ratio`, when present, is between `0.1` and `1.0`
- every R2 target declares `account_secret`, `access_key_secret`, `secret_key_secret`, `bucket_secret`, and `prefix`

Update `main()` so if the local YAML has `delta_backup:` it serializes the validated object into a GitHub Actions secret named `DELTA_BACKUP_CONFIG`.

- [ ] **Step 3: Update example config**

Modify `config/backup-config.example.yml` to keep the legacy `DB_BACKUP_CONFIG` example and add:

```yaml
  # Delta-transfer pipeline config. Stored as DELTA_BACKUP_CONFIG for GitHub Actions.
  DELTA_BACKUP_CONFIG:
    backup_databases:
      - name: prod-a
        url_secret: PROD_A_DATABASE_URL
        schedule_group: daytime-2h
        primary_target: r2-primary-prod-a
        mirror_targets:
          - r2-secondary-prod-a
        delta:
          enabled: true
          max_delta_ratio: 0.70
      - name: prod-b
        url_secret: PROD_B_DATABASE_URL
        schedule_group: daytime-2h-offset-15m
        primary_target: r2-primary-prod-b
        mirror_targets: []
        delta:
          enabled: true
          max_delta_ratio: 0.70
    r2_targets:
      r2-primary-prod-a:
        account_secret: R2_PRIMARY_ACCOUNT_ID
        access_key_secret: R2_PRIMARY_ACCESS_KEY_ID
        secret_key_secret: R2_PRIMARY_SECRET_ACCESS_KEY
        bucket_secret: R2_PRIMARY_BUCKET_NAME
        prefix: prod-a/
      r2-secondary-prod-a:
        account_secret: R2_SECONDARY_ACCOUNT_ID
        access_key_secret: R2_SECONDARY_ACCESS_KEY_ID
        secret_key_secret: R2_SECONDARY_SECRET_ACCESS_KEY
        bucket_secret: R2_SECONDARY_BUCKET_NAME
        prefix: prod-a/
      r2-primary-prod-b:
        account_secret: R2_PRIMARY_ACCOUNT_ID
        access_key_secret: R2_PRIMARY_ACCESS_KEY_ID
        secret_key_secret: R2_PRIMARY_SECRET_ACCESS_KEY
        bucket_secret: R2_PRIMARY_BUCKET_NAME
        prefix: prod-b/
```

- [ ] **Step 4: Run tests**

Run:

```bash
timeout 60s python -m pytest tests/test_apply_gh_actions_config.py -q
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add config/backup-config.example.yml scripts/apply-gh-actions-config.py tests/test_apply_gh_actions_config.py
git commit -m "feat: add delta backup config validation"
```

## Task 7: Documentation, Rollout, and Verification

**Files:**
- Create: `README.md` if missing; otherwise modify it
- Modify: `AGENTS.md` only if command guidance changes materially
- Modify: `docs/superpowers/specs/2026-05-07-delta-transfer-r2-backup-design.md` only if implementation reveals a spec correction

- [ ] **Step 1: Document server-side command**

Add a README section with this exact shape:

````markdown
## Delta-transfer backups

The delta-transfer pipeline keeps server egress low while publishing full restore files to R2.

Server-side scheduled command:

```bash
python scripts/create_delta_backup.py \
  --config config/backup-config.local.yml \
  --db-name prod-a \
  --db-url-env PROD_A_DATABASE_URL \
  --state-root /data/delta-backup-state
```

The server only uploads staging objects to primary R2. GitHub Actions publishes verified full backups and mirrors official full backups to secondary R2.
````

- [ ] **Step 2: Document recovery command**

Add:

````markdown
Restore a verified full backup:

```bash
aws s3 cp "s3://<bucket>/<db>/full/<backup>.dump.zst" .
zstd -d "<backup>.dump.zst" -o "<backup>.dump"
pg_restore --clean --if-exists --no-owner --no-acl --dbname="$RESTORE_URL" "<backup>.dump"
```
````

- [ ] **Step 3: Document rollout**

Add:

````markdown
Recommended rollout:

1. Run one database with `--dry-run`.
2. Run one full staging publication.
3. Run one delta publication.
4. Restore the published `.dump.zst` into a temporary PostgreSQL instance.
5. Enable the server schedule for all databases with staggered start times.
6. Keep the legacy workflow enabled until at least two successful delta-transfer restore checks pass.
````

- [ ] **Step 4: Run full local verification**

Run:

```bash
timeout 60s python -m pytest tests -q
actionlint .github/workflows/backup-to-r2.yml .github/workflows/delta-backup-publish.yml .github/workflows/delta-backup-mirror.yml
```

Expected: PASS. If `actionlint` is missing, install it or state that only Python tests ran.

- [ ] **Step 5: Commit**

```bash
git add README.md AGENTS.md docs/superpowers/specs/2026-05-07-delta-transfer-r2-backup-design.md
git commit -m "docs: document delta backup rollout"
```

## NOT in Scope

- Replacing the legacy `backup-to-r2.yml` immediately: keep it until the delta-transfer pipeline has at least two verified restore checks.
- WAL/PITR: deferred because the accepted design avoids Postgres container changes.
- Cloudflare Worker-based cross-account copying: deferred because GitHub Actions is already used and is simpler for first-stage implementation.
- A long-lived daemon: deferred; server-side scheduling can call the script by cron/Zeabur scheduled command.

## Test Coverage Map

```text
CODE PATH COVERAGE TO BUILD
===========================
[+] scripts/delta_backup_lib.py
    ├── sha256_file()                         -> unit test exact known hash
    ├── load_json()/dump_json()               -> unit test round trip and non-object rejection
    ├── R2Target.s3_uri()/aws_env()           -> unit test env validation and prefix behavior
    └── aws upload/download wrappers          -> unit test command construction via monkeypatch

[+] scripts/create_delta_backup.py
    ├── no base + no latest                   -> full staging test
    ├── base exists + small delta             -> delta staging test
    ├── base exists + large delta             -> full fallback test
    ├── pending manifest exists               -> skip and alert test
    ├── local base missing + latest exists    -> download/validate base test
    └── upload fails before manifest          -> no pending_manifest write test

[+] scripts/publish_delta_backup.py
    ├── delta manifest success                -> rebuild + validate + publish test
    ├── full manifest success                 -> validate + publish test
    ├── bad archive hash                      -> fail without latest update test
    ├── bad dump hash                         -> fail without latest update test
    ├── pg_restore --list fails               -> fail without latest update test
    └── latest changed since manifest         -> stop without publish test

[+] scripts/mirror_r2_full.py
    ├── full/*.dump.zst                       -> mirror test
    ├── full/latest.json                      -> mirror test
    ├── staging/*                             -> skip test
    └── latest older than max lag             -> fail test
```

## Final Verification Before Shipping

```bash
timeout 60s python -m pytest tests -q
actionlint .github/workflows/backup-to-r2.yml .github/workflows/delta-backup-publish.yml .github/workflows/delta-backup-mirror.yml
python scripts/apply-gh-actions-config.py --config config/backup-config.example.yml --dry-run
```

Expected:

- all Python tests pass
- workflow lint passes
- config dry-run prints `DELTA_BACKUP_CONFIG` in the secrets list

## GSTACK REVIEW REPORT

| Review | Trigger | Why | Runs | Status | Findings |
|--------|---------|-----|------|--------|----------|
| CEO Review | `/plan-ceo-review` | Scope & strategy | 0 | — | — |
| Codex Review | `/codex review` | Independent 2nd opinion | 0 | — | — |
| Eng Review | `/plan-eng-review` | Architecture & tests (required) | 2 | issues_open | 4 plan-interface issues, 0 critical architecture gaps |
| Design Review | `/plan-design-review` | UI/UX gaps | 0 | — | — |

**UNRESOLVED:** 4 plan edits should be applied before implementation.

**VERDICT:** ENG REVIEW HAS PLAN FIXES — architecture is acceptable, but implementation should not start until the CLI/config/test mismatches below are corrected.

Review findings:

1. `publish_delta_backup.py` interface does not match the workflow. The plan says the CLI accepts `--config`, `--db-name`, and `--staging-run-id`, but the workflow calls only `--config-env` and `--work-dir`; either the script must scan staging objects from config, or the workflow must pass explicit db/run inputs.
2. `DELTA_BACKUP_CONFIG` config shape is inconsistent. Task 6 says the local YAML may have `delta_backup:`, but the example uses `secrets.DELTA_BACKUP_CONFIG`; pick one shape so `apply-gh-actions-config.py --dry-run` can actually emit the expected secret.
3. Tests cannot import `scripts.apply_gh_actions_config` while the existing file is named `scripts/apply-gh-actions-config.py`; either load it by file path in tests or add an importable wrapper module.
4. Server-side config loading is unclear. The README command passes `--config config/backup-config.local.yml`, but the runtime dependency list only installs PyYAML for dev; either make runtime scripts consume JSON from `DELTA_BACKUP_CONFIG` or document/install PyYAML for server use.
