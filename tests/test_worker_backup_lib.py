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
