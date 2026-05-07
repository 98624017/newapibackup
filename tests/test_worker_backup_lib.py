from __future__ import annotations

import json

import pytest

from scripts.worker_backup_lib import R2Target, dump_json, load_config_from_env, load_json, sha256_file


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


def test_load_config_from_env_reads_json(monkeypatch):
    monkeypatch.setenv("BACKUP_CONFIG", '{"backup_databases": [], "r2_targets": {}}')

    assert load_config_from_env("BACKUP_CONFIG") == {"backup_databases": [], "r2_targets": {}}


def test_load_config_from_env_reports_env_name_for_bad_json(monkeypatch):
    monkeypatch.setenv("BACKUP_CONFIG", "{")

    with pytest.raises(ValueError, match="BACKUP_CONFIG must contain valid JSON"):
        load_config_from_env("BACKUP_CONFIG")


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


def test_r2_target_allows_empty_prefix(monkeypatch):
    monkeypatch.setenv("R2_ACCOUNT_ID", "acct")
    monkeypatch.setenv("R2_BUCKET", "bucket")
    target = R2Target(
        name="primary",
        account_env="R2_ACCOUNT_ID",
        access_key_env="R2_ACCESS_KEY_ID",
        secret_key_env="R2_SECRET_ACCESS_KEY",
        bucket_env="R2_BUCKET",
        prefix="",
    )

    assert target.object_key("full/latest.json") == "full/latest.json"
    assert target.s3_uri("full/latest.json") == "s3://bucket/full/latest.json"
