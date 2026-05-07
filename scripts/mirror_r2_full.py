#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime as dt
import json
import subprocess
import tempfile
from pathlib import Path
from typing import Any

try:
    from scripts.worker_backup_lib import R2Target, load_config_from_env, run_cmd
except ModuleNotFoundError:
    from worker_backup_lib import R2Target, load_config_from_env, run_cmd


def should_mirror_key(key: str) -> bool:
    candidate = key
    if not candidate.startswith("full/") and "/full/" in candidate:
        candidate = f"full/{candidate.split('/full/', 1)[1]}"
    return candidate.startswith("full/") and (
        candidate.endswith(".sql.gz") or candidate.endswith(".sql.gz.json") or candidate.endswith("/latest.json")
    )


def assert_latest_fresh(latest: dict[str, Any], *, now: dt.datetime, max_lag_hours: int) -> None:
    created_at = latest.get("created_at")
    if not isinstance(created_at, str) or not created_at:
        raise ValueError("latest.json missing created_at")
    parsed = dt.datetime.fromisoformat(created_at)
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=dt.timezone.utc)
    if now - parsed > dt.timedelta(hours=max_lag_hours):
        raise ValueError(f"latest backup is stale: {created_at}")


def target_from_config(name: str, r2_targets: dict[str, dict[str, str]]) -> R2Target:
    config = r2_targets.get(name)
    if not config:
        raise ValueError(f"Unknown R2 target: {name}")
    return R2Target(name=name, **config)


def list_keys(target: R2Target, prefix: str) -> list[str]:
    cmd = ["aws", "s3api", "list-objects-v2", "--bucket", target.bucket, "--endpoint-url", target.endpoint]
    if prefix:
        cmd.extend(["--prefix", prefix])
    result = run_cmd(cmd, env=target.aws_env())
    data = json.loads(result.stdout or "{}")
    return [item["Key"] for item in data.get("Contents", []) if isinstance(item.get("Key"), str)]


def object_size(target: R2Target, key: str) -> int | None:
    try:
        result = run_cmd(
            ["aws", "s3api", "head-object", "--bucket", target.bucket, "--key", key, "--endpoint-url", target.endpoint],
            env=target.aws_env(),
        )
    except subprocess.CalledProcessError:
        return None
    data = json.loads(result.stdout or "{}")
    size = data.get("ContentLength")
    return int(size) if isinstance(size, int) else None


def relative_key_for_target(target: R2Target, key: str) -> str | None:
    prefix = target.prefix.strip("/")
    if not prefix:
        return key.lstrip("/")
    prefix_with_slash = f"{prefix}/"
    if not key.startswith(prefix_with_slash):
        return None
    return key[len(prefix_with_slash) :]


def copy_object(primary: R2Target, secondary: R2Target, primary_key: str, secondary_key: str, temp_dir: Path) -> None:
    local_path = temp_dir / Path(primary_key).name
    run_cmd(["aws", "s3", "cp", f"s3://{primary.bucket}/{primary_key}", str(local_path), "--endpoint-url", primary.endpoint], env=primary.aws_env())
    run_cmd(["aws", "s3", "cp", str(local_path), f"s3://{secondary.bucket}/{secondary_key}", "--endpoint-url", secondary.endpoint], env=secondary.aws_env())


def mirror_database(db_config: dict[str, Any], r2_targets: dict[str, dict[str, str]], *, max_lag_hours: int) -> int:
    primary = target_from_config(str(db_config["primary_target"]), r2_targets)
    mirror_targets = db_config.get("mirror_targets", [])
    if not mirror_targets:
        return 0

    keys: list[tuple[str, str]] = []
    for key in list_keys(primary, primary.prefix.strip("/")):
        relative_key = relative_key_for_target(primary, key)
        if relative_key and should_mirror_key(relative_key):
            keys.append((key, relative_key))
    latest_key = primary.object_key("full/latest.json")
    if latest_key not in {key for key, _relative_key in keys}:
        raise ValueError(f"Missing latest.json for {db_config['name']}: {latest_key}")

    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        latest_path = tmp_path / "latest.json"
        run_cmd(["aws", "s3", "cp", f"s3://{primary.bucket}/{latest_key}", str(latest_path), "--endpoint-url", primary.endpoint], env=primary.aws_env())
        assert_latest_fresh(json.loads(latest_path.read_text(encoding="utf-8")), now=dt.datetime.now(dt.timezone.utc), max_lag_hours=max_lag_hours)

        copied = 0
        for mirror_target_name in mirror_targets:
            secondary = target_from_config(str(mirror_target_name), r2_targets)
            for primary_key, relative_key in keys:
                secondary_key = secondary.object_key(relative_key)
                primary_size = object_size(primary, primary_key)
                secondary_size = object_size(secondary, secondary_key)
                if primary_size is not None and primary_size == secondary_size:
                    continue
                copy_object(primary, secondary, primary_key, secondary_key, tmp_path)
                copied += 1
        return copied


def main() -> int:
    parser = argparse.ArgumentParser(description="Mirror verified full backups from primary R2 to secondary R2.")
    parser.add_argument("--config-env", default="BACKUP_WORKER_CONFIG")
    parser.add_argument("--max-lag-hours", type=int, default=6)
    args = parser.parse_args()

    config = load_config_from_env(args.config_env)
    copied = 0
    for db_config in config.get("backup_databases", []):
        copied += mirror_database(db_config, config.get("r2_targets", {}), max_lag_hours=args.max_lag_hours)
    print(f"Mirrored {copied} objects")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
