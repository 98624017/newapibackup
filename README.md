# PostgreSQL R2 Backups

这个仓库包含两条备份路径：

- 现有 GitHub Actions 全量备份：`.github/workflows/backup-to-r2.yml`，先保留作为 fallback。
- 推荐的服务器侧 worker：在 PostgreSQL 同机或同 Zeabur 环境执行 `pg_dump | gzip -9`，只上传一份压缩后的 `.sql.gz` 到 primary R2，再由 GitHub Actions 每 4 小时镜像到 secondary R2。

## Zeabur Worker Backups

worker 运行环境需要安装：

- Python 3
- PostgreSQL client tools，至少包含 `pg_dump`
- `gzip`
- AWS CLI v2

服务器或 Zeabur 服务里配置这些环境变量：

```bash
export BACKUP_WORKER_CONFIG="$(cat config/backup-worker-config.example.json)"
export PROD_A_DATABASE_URL="postgres://user:password@postgres.internal:5432/prod_a"
export R2_PRIMARY_ACCOUNT_ID="<cloudflare-account-id>"
export R2_PRIMARY_ACCESS_KEY_ID="<r2-access-key>"
export R2_PRIMARY_SECRET_ACCESS_KEY="<r2-secret-key>"
export R2_PRIMARY_BUCKET_NAME="<primary-bucket>"
```

执行单库备份：

```bash
python scripts/zeabur_backup_worker.py \
  --config-env BACKUP_WORKER_CONFIG \
  --db-name prod-a \
  --state-root /data/backup-worker
```

执行全部配置库：

```bash
python scripts/zeabur_backup_worker.py \
  --config-env BACKUP_WORKER_CONFIG \
  --state-root /data/backup-worker
```

备份文件会上传到：

```text
s3://<primary-bucket>/<prefix>/full/YYYY/MM/<db>-backup-YYYYMMDD-HHMMSS.sql.gz
s3://<primary-bucket>/<prefix>/full/YYYY/MM/<db>-backup-YYYYMMDD-HHMMSS.sql.gz.json
s3://<primary-bucket>/<prefix>/full/latest.json
```

调度频率由外部 cron、Zeabur scheduled service 或同机进程控制。当前可以先保持每天 2 次，确认 worker 备份至少恢复成功 2 次后，再提高到白天每 2 小时一次。

## Secondary Mirror

`.github/workflows/mirror-r2-full.yml` 每 4 小时把 primary R2 的正式 full 备份镜像到 secondary R2。它只复制：

- `full/*.sql.gz`
- `full/*.sql.gz.json`
- `full/latest.json`

如果 secondary 已存在同 size 对象，会跳过复制。`full/latest.json` 超过 `--max-lag-hours` 会让 workflow 失败，用来暴露 worker 停跑或上传失败。

GitHub Secrets 需要配置：

```text
BACKUP_WORKER_CONFIG
R2_ACCOUNT_ID
R2_ACCESS_KEY_ID
R2_SECRET_ACCESS_KEY
R2_BUCKET_NAME
R2_2_ACCOUNT_ID
R2_2_ACCESS_KEY_ID
R2_2_SECRET_ACCESS_KEY
R2_2_BUCKET_NAME
```

其中 `BACKUP_WORKER_CONFIG` 可以复用 `config/backup-worker-config.example.json` 的结构；workflow 会把现有 `R2_*` secrets 映射为配置里的 `R2_PRIMARY_*` 和 `R2_SECONDARY_*` 环境变量。

## Restore

下载并校验 gzip：

```bash
aws s3 cp "s3://<bucket>/<db>/full/YYYY/MM/<backup>.sql.gz" .
gzip -t "<backup>.sql.gz"
```

恢复到目标库：

```bash
gzip -dc "<backup>.sql.gz" | psql "$RESTORE_URL"
```

`pg_dump` 使用 plain SQL、`--clean --if-exists --no-owner --no-acl`。恢复前建议先在临时数据库演练，确认应用版本和扩展环境一致。

## Validation

```bash
timeout 60s python -m pytest tests -q
actionlint .github/workflows/backup-to-r2.yml .github/workflows/mirror-r2-full.yml
```

如果本机没有安装 `actionlint`，以 GitHub Actions 实跑结果作为 workflow 语法验证。
