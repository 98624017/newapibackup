from __future__ import annotations

import datetime as dt

import pytest

from scripts.zeabur_backup_worker import (
    backup_database,
    build_backup_key,
    build_manifest,
    main,
    pg_dump_command,
    select_databases,
)


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
        "prod-a",
        created_at,
        "prod-a/full/2026/05/prod-a-backup-20260507-143000.sql.gz",
        "abc",
        18,
    )

    assert manifest["db"] == "prod-a"
    assert manifest["sha256"] == "abc"
    assert manifest["size"] == 18
    assert manifest["format"] == "plain sql gzip"


def test_select_databases_filters_by_name():
    databases = [{"name": "prod-a"}, {"name": "prod-b"}]

    assert select_databases(databases, "prod-b") == [{"name": "prod-b"}]


def test_dry_run_does_not_upload(tmp_path, monkeypatch):
    uploads: list[str] = []

    def fake_run_cmd(cmd):
        output_path = cmd[-1]
        with open(output_path, "wb") as fh:
            fh.write(b"backup")

    monkeypatch.setenv("PROD_A_DATABASE_URL", "postgres://example")
    monkeypatch.setattr("scripts.zeabur_backup_worker.run_cmd", fake_run_cmd)
    monkeypatch.setattr("scripts.zeabur_backup_worker.upload_file", lambda *args: uploads.append(args[2]))

    backup_database(
        db_config={"name": "prod-a", "url_env": "PROD_A_DATABASE_URL", "primary_target": "r2-primary"},
        r2_targets={
            "r2-primary": {
                "account_env": "R2_ACCOUNT_ID",
                "access_key_env": "R2_ACCESS_KEY_ID",
                "secret_key_env": "R2_SECRET_ACCESS_KEY",
                "bucket_env": "R2_BUCKET",
                "prefix": "prod-a/",
            }
        },
        state_root=tmp_path,
        created_at=dt.datetime(2026, 5, 7, 14, 30, tzinfo=dt.timezone.utc),
        dry_run=True,
    )

    assert uploads == []


def test_upload_order_is_backup_manifest_latest(tmp_path, monkeypatch):
    uploads: list[str] = []

    def fake_run_cmd(cmd):
        output_path = cmd[-1]
        with open(output_path, "wb") as fh:
            fh.write(b"backup")

    monkeypatch.setenv("PROD_A_DATABASE_URL", "postgres://example")
    monkeypatch.setenv("R2_ACCOUNT_ID", "acct")
    monkeypatch.setenv("R2_ACCESS_KEY_ID", "key")
    monkeypatch.setenv("R2_SECRET_ACCESS_KEY", "secret")
    monkeypatch.setenv("R2_BUCKET", "bucket")
    monkeypatch.setattr("scripts.zeabur_backup_worker.run_cmd", fake_run_cmd)
    monkeypatch.setattr("scripts.zeabur_backup_worker.upload_file", lambda *args: uploads.append(args[2]))

    backup_database(
        db_config={"name": "prod-a", "url_env": "PROD_A_DATABASE_URL", "primary_target": "r2-primary"},
        r2_targets={
            "r2-primary": {
                "account_env": "R2_ACCOUNT_ID",
                "access_key_env": "R2_ACCESS_KEY_ID",
                "secret_key_env": "R2_SECRET_ACCESS_KEY",
                "bucket_env": "R2_BUCKET",
                "prefix": "prod-a/",
            }
        },
        state_root=tmp_path,
        created_at=dt.datetime(2026, 5, 7, 14, 30, tzinfo=dt.timezone.utc),
        dry_run=False,
    )

    assert uploads == [
        "full/2026/05/prod-a-backup-20260507-143000.sql.gz",
        "full/2026/05/prod-a-backup-20260507-143000.sql.gz.json",
        "full/latest.json",
    ]
    assert not (tmp_path / "prod-a" / "latest.json").exists()


def test_main_db_name_only_backs_up_selected_database(tmp_path, monkeypatch):
    backed_up: list[str] = []
    config = {
        "backup_databases": [
            {"name": "prod-a", "url_env": "PROD_A_DATABASE_URL", "primary_target": "r2-primary"},
            {"name": "prod-b", "url_env": "PROD_B_DATABASE_URL", "primary_target": "r2-primary"},
        ],
        "r2_targets": {
            "r2-primary": {
                "account_env": "R2_ACCOUNT_ID",
                "access_key_env": "R2_ACCESS_KEY_ID",
                "secret_key_env": "R2_SECRET_ACCESS_KEY",
                "bucket_env": "R2_BUCKET",
                "prefix": "backups/",
            }
        },
    }

    monkeypatch.setattr("sys.argv", ["zeabur_backup_worker.py", "--db-name", "prod-b", "--state-root", str(tmp_path)])
    monkeypatch.setattr("scripts.zeabur_backup_worker.load_config_from_env", lambda env_name: config)

    def fake_backup_database(*, db_config, **kwargs):
        backed_up.append(db_config["name"])
        return {"object": db_config["name"], "size": 1}

    monkeypatch.setattr("scripts.zeabur_backup_worker.backup_database", fake_backup_database)

    assert main() == 0
    assert backed_up == ["prod-b"]


def test_upload_failure_cleans_local_temp_files(tmp_path, monkeypatch):
    def fake_run_cmd(cmd):
        output_path = cmd[-1]
        with open(output_path, "wb") as fh:
            fh.write(b"backup")

    monkeypatch.setenv("PROD_A_DATABASE_URL", "postgres://example")
    monkeypatch.setenv("R2_ACCOUNT_ID", "acct")
    monkeypatch.setenv("R2_ACCESS_KEY_ID", "key")
    monkeypatch.setenv("R2_SECRET_ACCESS_KEY", "secret")
    monkeypatch.setenv("R2_BUCKET", "bucket")
    monkeypatch.setattr("scripts.zeabur_backup_worker.run_cmd", fake_run_cmd)
    monkeypatch.setattr(
        "scripts.zeabur_backup_worker.upload_file",
        lambda *args: (_ for _ in ()).throw(RuntimeError("upload failed")),
    )

    with pytest.raises(RuntimeError, match="upload failed"):
        backup_database(
            db_config={"name": "prod-a", "url_env": "PROD_A_DATABASE_URL", "primary_target": "r2-primary"},
            r2_targets={
                "r2-primary": {
                    "account_env": "R2_ACCOUNT_ID",
                    "access_key_env": "R2_ACCESS_KEY_ID",
                    "secret_key_env": "R2_SECRET_ACCESS_KEY",
                    "bucket_env": "R2_BUCKET",
                    "prefix": "prod-a/",
                }
            },
            state_root=tmp_path,
            created_at=dt.datetime(2026, 5, 7, 14, 30, tzinfo=dt.timezone.utc),
            dry_run=False,
        )

    assert list((tmp_path / "prod-a").glob("*")) == []
