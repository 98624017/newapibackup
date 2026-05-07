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
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ValueError(f"{env_name} must contain valid JSON.") from exc
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
        prefix = self.prefix.strip("/")
        key = relative_key.lstrip("/")
        if not prefix:
            return key
        return f"{prefix}/{key}"

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
