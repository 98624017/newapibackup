from __future__ import annotations

import datetime as dt
import json
import subprocess

import pytest

from scripts.mirror_r2_full import assert_latest_fresh, mirror_database, object_size, relative_key_for_target, should_mirror_key
from scripts.worker_backup_lib import R2Target


def test_should_mirror_full_backup_and_metadata():
    assert should_mirror_key("prod-a/full/2026/05/prod-a-backup-20260507-143000.sql.gz") is True
    assert should_mirror_key("prod-a/full/2026/05/prod-a-backup-20260507-143000.sql.gz.json") is True
    assert should_mirror_key("prod-a/full/latest.json") is True


def test_should_not_mirror_non_full_objects():
    assert should_mirror_key("prod-a/tmp/file.sql.gz") is False
    assert should_mirror_key("prod-a/staging/run/manifest.json") is False


def test_assert_latest_fresh_accepts_recent_latest():
    latest = {"created_at": "2026-05-07T12:00:00+00:00"}
    now = dt.datetime(2026, 5, 7, 16, 0, tzinfo=dt.timezone.utc)

    assert_latest_fresh(latest, now=now, max_lag_hours=6)


def test_assert_latest_fresh_rejects_stale_latest():
    latest = {"created_at": "2026-05-07T09:00:00+00:00"}
    now = dt.datetime(2026, 5, 7, 16, 0, tzinfo=dt.timezone.utc)

    with pytest.raises(ValueError, match="latest backup is stale"):
        assert_latest_fresh(latest, now=now, max_lag_hours=6)


def test_relative_key_for_target_strips_target_prefix():
    target = R2Target(
        name="primary",
        account_env="R2_ACCOUNT_ID",
        access_key_env="R2_ACCESS_KEY_ID",
        secret_key_env="R2_SECRET_ACCESS_KEY",
        bucket_env="R2_BUCKET",
        prefix="primary-prod-a/",
    )

    assert relative_key_for_target(target, "primary-prod-a/full/latest.json") == "full/latest.json"
    assert relative_key_for_target(target, "other/full/latest.json") is None


def test_object_size_returns_none_when_object_is_missing(monkeypatch):
    target = R2Target(
        name="secondary",
        account_env="R2_ACCOUNT_ID",
        access_key_env="R2_ACCESS_KEY_ID",
        secret_key_env="R2_SECRET_ACCESS_KEY",
        bucket_env="R2_BUCKET",
        prefix="",
    )
    monkeypatch.setenv("R2_ACCOUNT_ID", "acct")
    monkeypatch.setenv("R2_BUCKET", "bucket")
    monkeypatch.setenv("R2_ACCESS_KEY_ID", "key")
    monkeypatch.setenv("R2_SECRET_ACCESS_KEY", "secret")

    def fake_run_cmd(cmd, *, env=None):
        raise subprocess.CalledProcessError(returncode=1, cmd=cmd, stderr="not found")

    monkeypatch.setattr("scripts.mirror_r2_full.run_cmd", fake_run_cmd)

    assert object_size(target, "full/missing.sql.gz") is None


def test_mirror_database_maps_primary_prefix_to_secondary_prefix(monkeypatch):
    for name in [
        "PRIMARY_ACCOUNT",
        "PRIMARY_KEY",
        "PRIMARY_SECRET",
        "PRIMARY_BUCKET",
        "SECONDARY_ACCOUNT",
        "SECONDARY_KEY",
        "SECONDARY_SECRET",
        "SECONDARY_BUCKET",
    ]:
        monkeypatch.setenv(name, name.lower())

    copied_to_secondary: list[str] = []
    fresh_latest = json.dumps({"created_at": "2026-05-07T12:00:00+00:00"})

    def fake_run_cmd(cmd, *, env=None):
        if cmd[:3] == ["aws", "s3api", "list-objects-v2"]:
            return subprocess.CompletedProcess(
                cmd,
                0,
                stdout=json.dumps(
                    {
                        "Contents": [
                            {"Key": "primary-prod-a/full/latest.json"},
                            {"Key": "primary-prod-a/full/2026/05/prod-a-backup-20260507-120000.sql.gz"},
                            {"Key": "primary-prod-a/tmp/prod-a.sql.gz"},
                        ]
                    }
                ),
                stderr="",
            )
        if cmd[:3] == ["aws", "s3api", "head-object"]:
            key = cmd[cmd.index("--key") + 1]
            sizes = {
                "primary-prod-a/full/latest.json": 18,
                "secondary-prod-a/full/latest.json": 18,
                "primary-prod-a/full/2026/05/prod-a-backup-20260507-120000.sql.gz": 100,
                "secondary-prod-a/full/2026/05/prod-a-backup-20260507-120000.sql.gz": 50,
            }
            return subprocess.CompletedProcess(cmd, 0, stdout=json.dumps({"ContentLength": sizes[key]}), stderr="")
        if cmd[:3] == ["aws", "s3", "cp"] and str(cmd[3]).endswith("full/latest.json"):
            with open(cmd[4], "w", encoding="utf-8") as fh:
                fh.write(fresh_latest)
            return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
        if cmd[:3] == ["aws", "s3", "cp"] and str(cmd[3]).startswith("s3://primary_bucket/"):
            with open(cmd[4], "wb") as fh:
                fh.write(b"backup")
            return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
        if cmd[:3] == ["aws", "s3", "cp"] and str(cmd[4]).startswith("s3://secondary_bucket/"):
            copied_to_secondary.append(cmd[4])
            return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
        raise AssertionError(f"unexpected command: {cmd}")

    monkeypatch.setattr("scripts.mirror_r2_full.run_cmd", fake_run_cmd)
    monkeypatch.setattr("scripts.mirror_r2_full.dt.datetime", FixedDateTime)

    copied = mirror_database(
        {
            "name": "prod-a",
            "primary_target": "primary",
            "mirror_targets": ["secondary"],
        },
        {
            "primary": {
                "account_env": "PRIMARY_ACCOUNT",
                "access_key_env": "PRIMARY_KEY",
                "secret_key_env": "PRIMARY_SECRET",
                "bucket_env": "PRIMARY_BUCKET",
                "prefix": "primary-prod-a/",
            },
            "secondary": {
                "account_env": "SECONDARY_ACCOUNT",
                "access_key_env": "SECONDARY_KEY",
                "secret_key_env": "SECONDARY_SECRET",
                "bucket_env": "SECONDARY_BUCKET",
                "prefix": "secondary-prod-a/",
            },
        },
        max_lag_hours=6,
    )

    assert copied == 1
    assert copied_to_secondary == [
        "s3://secondary_bucket/secondary-prod-a/full/2026/05/prod-a-backup-20260507-120000.sql.gz"
    ]


class FixedDateTime(dt.datetime):
    @classmethod
    def now(cls, tz=None):
        return cls(2026, 5, 7, 16, 0, tzinfo=tz)
