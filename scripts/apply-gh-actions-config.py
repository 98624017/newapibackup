#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path
from typing import Any


ALLOWED_TARGETS = {"primary", "secondary", "both"}


def _run(cmd: list[str], *, input_text: str | None = None) -> None:
    subprocess.run(
        cmd,
        input=input_text,
        text=True,
        check=True,
        stdout=sys.stdout,
        stderr=sys.stderr,
    )


def _detect_repo_from_git_remote() -> str | None:
    try:
        result = subprocess.run(
            ["git", "remote", "get-url", "origin"],
            check=True,
            capture_output=True,
            text=True,
        )
    except Exception:
        return None

    url = result.stdout.strip()
    if not url:
        return None

    # https://github.com/OWNER/REPO(.git)
    https_prefix = "https://github.com/"
    if url.startswith(https_prefix):
        path = url[len(https_prefix) :]
        if path.endswith(".git"):
            path = path[: -len(".git")]
        if "/" in path:
            return path
        return None

    # git@github.com:OWNER/REPO(.git)
    ssh_prefix = "git@github.com:"
    if url.startswith(ssh_prefix):
        path = url[len(ssh_prefix) :]
        if path.endswith(".git"):
            path = path[: -len(".git")]
        if "/" in path:
            return path
        return None

    return None


def _load_config(path: Path) -> dict[str, Any]:
    raw = path.read_text(encoding="utf-8")
    if path.suffix in {".yml", ".yaml"}:
        try:
            import yaml  # type: ignore
        except Exception as exc:  # pragma: no cover
            raise RuntimeError("Missing dependency: PyYAML is required for .yml/.yaml configs") from exc
        data = yaml.safe_load(raw)
    else:
        data = json.loads(raw)

    if not isinstance(data, dict):
        raise ValueError("Config must be a JSON/YAML object at the top level.")
    return data


def _normalize_targets(value: str) -> str:
    normalized = value.strip().lower()
    if normalized not in ALLOWED_TARGETS:
        raise ValueError(f"Invalid targets: {value!r} (allowed: primary|secondary|both)")
    return normalized


def _validate_db_backup_config(items: Any) -> list[dict[str, Any]]:
    if not isinstance(items, list):
        raise ValueError("secrets.DB_BACKUP_CONFIG must be a list of database items.")

    validated: list[dict[str, Any]] = []
    for i, item in enumerate(items):
        if not isinstance(item, dict):
            raise ValueError(f"DB_BACKUP_CONFIG[{i}] must be an object.")

        name = item.get("name")
        url = item.get("url")
        if not isinstance(name, str) or not name.strip():
            raise ValueError(f"DB_BACKUP_CONFIG[{i}].name is required.")
        if name.strip() == "...":
            raise ValueError(f"DB_BACKUP_CONFIG[{i}].name is still '...'; please fill it.")
        if not isinstance(url, str) or not url.strip():
            raise ValueError(f"DB_BACKUP_CONFIG[{i}].url is required.")
        if url.strip() == "...":
            raise ValueError(f"DB_BACKUP_CONFIG[{i}].url is still '...'; please fill it.")

        targets = item.get("targets")
        if targets is not None:
            if not isinstance(targets, str):
                raise ValueError(f"DB_BACKUP_CONFIG[{i}].targets must be a string.")
            item["targets"] = _normalize_targets(targets)

        validated.append(item)

    return validated


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Apply GitHub Actions repository secrets/variables from a local config file."
    )
    parser.add_argument(
        "--config",
        required=True,
        help="Path to config file (YAML/JSON). Recommended: config/backup-config.local.yml",
    )
    parser.add_argument(
        "--repo",
        default="",
        help="Target repo in OWNER/REPO format. If omitted, uses config.repo, then git remote origin.",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print what would be changed without writing to GitHub.",
    )
    args = parser.parse_args()

    config_path = Path(args.config)
    if not config_path.exists():
        print(f"Config file not found: {config_path}", file=sys.stderr)
        return 2

    config = _load_config(config_path)

    repo = (args.repo or config.get("repo") or "").strip()
    if not repo:
        detected = _detect_repo_from_git_remote()
        if detected:
            repo = detected
    if not repo:
        print("Unable to determine repo. Pass --repo OWNER/REPO or set `repo:` in the config.", file=sys.stderr)
        return 2

    variables = config.get("variables") or {}
    if not isinstance(variables, dict):
        raise ValueError("variables must be an object.")

    secrets = config.get("secrets") or {}
    if not isinstance(secrets, dict):
        raise ValueError("secrets must be an object.")

    if "DB_BACKUP_CONFIG" not in secrets:
        raise ValueError("secrets.DB_BACKUP_CONFIG is required.")

    # Validate and normalize DB_BACKUP_CONFIG.
    secrets["DB_BACKUP_CONFIG"] = _validate_db_backup_config(secrets["DB_BACKUP_CONFIG"])

    # Validate global variable value if provided.
    if "R2_UPLOAD_TARGETS" in variables and variables["R2_UPLOAD_TARGETS"] is not None:
        if not isinstance(variables["R2_UPLOAD_TARGETS"], str):
            raise ValueError("variables.R2_UPLOAD_TARGETS must be a string.")
        variables["R2_UPLOAD_TARGETS"] = _normalize_targets(variables["R2_UPLOAD_TARGETS"])

    # Basic placeholder guard: prevent accidentally uploading template placeholders.
    for name, value in secrets.items():
        if name == "DB_BACKUP_CONFIG" or value is None:
            continue
        if not isinstance(value, str):
            raise ValueError(f"secrets.{name} must be a string (or omit it).")
        if value.strip() == "...":
            raise ValueError(f"secrets.{name} is still set to '...'; please fill it or remove the key.")

    # If any DB (or the global default) requires secondary, enforce secondary secrets are present.
    needs_secondary = False
    default_targets = variables.get("R2_UPLOAD_TARGETS")
    if isinstance(default_targets, str) and default_targets in {"secondary", "both"}:
        needs_secondary = True
    for item in secrets["DB_BACKUP_CONFIG"]:
        if item.get("targets") in {"secondary", "both"}:
            needs_secondary = True
            break

    if needs_secondary:
        required_secondary = [
            "R2_2_ACCESS_KEY_ID",
            "R2_2_SECRET_ACCESS_KEY",
            "R2_2_ACCOUNT_ID",
            "R2_2_BUCKET_NAME",
        ]
        for key in required_secondary:
            v = secrets.get(key)
            if not isinstance(v, str) or not v.strip() or v.strip() == "...":
                raise ValueError(f"secrets.{key} is required when using secondary/both targets.")

    print(f"Repo: {repo}")
    print(f"Config: {config_path}")

    secret_names = sorted(secrets.keys())
    variable_names = sorted(variables.keys())

    print("Will set secrets (Actions):")
    for name in secret_names:
        print(f"  - {name}")

    print("Will set variables:")
    for name in variable_names:
        print(f"  - {name}")

    if args.dry_run:
        print("Dry run: no changes applied.")
        return 0

    # Apply variables (non-sensitive).
    for name in variable_names:
        value = variables[name]
        if value is None:
            continue
        if not isinstance(value, str):
            raise ValueError(f"variables.{name} must be a string.")
        _run(["gh", "variable", "set", name, "-R", repo], input_text=value)

    # Apply secrets (sensitive). Values are passed via stdin (not CLI args).
    for name in secret_names:
        value = secrets[name]
        if value is None:
            continue
        if isinstance(value, (dict, list)):
            value_text = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
        else:
            if not isinstance(value, str):
                raise ValueError(f"secrets.{name} must be a string (or JSON for DB_BACKUP_CONFIG).")
            value_text = value
        _run(["gh", "secret", "set", name, "-R", repo, "--app", "actions"], input_text=value_text)

    print("Applied successfully.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
