#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime as dt
from pathlib import Path
from typing import Any

try:
    from scripts.worker_backup_lib import R2Target, dump_json, load_config_from_env, run_cmd, sha256_file
except ModuleNotFoundError:
    from worker_backup_lib import R2Target, dump_json, load_config_from_env, run_cmd, sha256_file


def build_backup_key(db_name: str, created_at: dt.datetime) -> str:
    return f"full/{created_at:%Y/%m}/{db_name}-backup-{created_at:%Y%m%d-%H%M%S}.sql.gz"


def pg_dump_command(db_url: str, output_path: str) -> list[str]:
    return [
        "bash",
        "-o",
        "pipefail",
        "-c",
        'pg_dump "$1" --format=plain --no-owner --no-acl --clean --if-exists | gzip -9 > "$2"',
        "pg-dump-pipe",
        db_url,
        output_path,
    ]


def build_manifest(
    *,
    db_name: str,
    created_at: dt.datetime,
    object_key: str,
    sha256: str,
    size: int,
) -> dict[str, Any]:
    return {
        "schema_version": 1,
        "db": db_name,
        "created_at": created_at.isoformat(),
        "object": object_key,
        "sha256": sha256,
        "size": size,
        "format": "plain sql gzip",
        "pg_dump": {
            "format": "plain",
            "no_owner": True,
            "no_acl": True,
            "clean": True,
            "if_exists": True,
        },
    }


def select_databases(databases: list[dict[str, Any]], db_name: str | None) -> list[dict[str, Any]]:
    if db_name is None:
        return databases
    selected = [db for db in databases if db.get("name") == db_name]
    if not selected:
        raise ValueError(f"Database not found in config: {db_name}")
    return selected


def target_from_config(name: str, r2_targets: dict[str, dict[str, str]]) -> R2Target:
    config = r2_targets.get(name)
    if not config:
        raise ValueError(f"Unknown R2 target: {name}")
    return R2Target(name=name, **config)


def upload_file(path: Path, target: R2Target, relative_key: str) -> None:
    run_cmd(["aws", "s3", "cp", str(path), target.s3_uri(relative_key), "--endpoint-url", target.endpoint], env=target.aws_env())


def backup_database(
    *,
    db_config: dict[str, Any],
    r2_targets: dict[str, dict[str, str]],
    state_root: Path,
    created_at: dt.datetime,
    dry_run: bool,
) -> dict[str, Any]:
    db_name = str(db_config["name"])
    db_url_env = str(db_config["url_env"])
    primary_target = str(db_config["primary_target"])

    import os

    db_url = os.environ.get(db_url_env, "")
    if not db_url:
        raise ValueError(f"Missing database URL env var: {db_url_env}")

    state_dir = state_root / db_name
    state_dir.mkdir(parents=True, exist_ok=True)

    backup_key = build_backup_key(db_name, created_at)
    object_key = target_from_config(primary_target, r2_targets).object_key(backup_key)
    local_backup = state_dir / Path(backup_key).name

    run_cmd(pg_dump_command(db_url, str(local_backup)))
    backup_sha256 = sha256_file(local_backup)
    manifest = build_manifest(
        db_name=db_name,
        created_at=created_at,
        object_key=object_key,
        sha256=backup_sha256,
        size=local_backup.stat().st_size,
    )
    manifest_path = state_dir / f"{local_backup.name}.json"
    latest_path = state_dir / "latest.json"
    dump_json(manifest_path, manifest)
    dump_json(latest_path, manifest)

    if not dry_run:
        target = target_from_config(primary_target, r2_targets)
        upload_file(local_backup, target, backup_key)
        upload_file(manifest_path, target, f"{backup_key}.json")
        upload_file(latest_path, target, "full/latest.json")

    local_backup.unlink(missing_ok=True)
    manifest_path.unlink(missing_ok=True)
    return manifest


def main() -> int:
    parser = argparse.ArgumentParser(description="Run PostgreSQL gzip backups from Zeabur/server side to primary R2.")
    parser.add_argument("--config-env", default="BACKUP_WORKER_CONFIG")
    parser.add_argument("--db-name", default=None)
    parser.add_argument("--state-root", default="./backup-worker-state")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    config = load_config_from_env(args.config_env)
    databases = select_databases(config.get("backup_databases", []), args.db_name)
    r2_targets = config.get("r2_targets", {})
    created_at = dt.datetime.now(dt.timezone.utc)

    for db_config in databases:
        manifest = backup_database(
            db_config=db_config,
            r2_targets=r2_targets,
            state_root=Path(args.state_root),
            created_at=created_at,
            dry_run=args.dry_run,
        )
        print(f"Backup prepared: {manifest['object']} ({manifest['size']} bytes)")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
