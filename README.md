# PostgreSQL R2 Backup Worker

轻量 PostgreSQL 备份服务，适合在每个 Zeabur 项目里单独部署一个 Docker 容器。一个容器只备份一个数据库，并把压缩后的 `.sql.gz` 上传到 Cloudflare R2。

## 运行方式

容器启动后会：

1. 读取环境变量。
2. 执行 `pg_dump`。
3. 用 gzip 压缩 dump 数据。
4. 上传 `.sql.gz` 到 R2。
5. 上传同名 manifest JSON。
6. 更新 `full/latest.json`。
7. 按 `BACKUP_INTERVAL_SECONDS` 进入下一轮。

默认启动后立即备份一次，之后每 12 小时备份一次。

## Zeabur 环境变量

必填：

```text
DATABASE_URL=postgres://user:password@host:5432/dbname
R2_ACCOUNT_ID=<cloudflare-account-id>
R2_ACCESS_KEY_ID=<r2-access-key>
R2_SECRET_ACCESS_KEY=<r2-secret-key>
R2_BUCKET_NAME=<r2-bucket>
```

可选：

```text
BACKUP_NAME=newapi
BACKUP_INTERVAL_SECONDS=43200
BACKUP_ON_START=true
R2_PREFIX=newapi
BACKUP_STATE_DIR=/tmp/backup-worker
```

建议在 Zeabur 同项目里部署本服务，并让 `DATABASE_URL` 使用 Zeabur 提供的内网 PostgreSQL 地址。

## Docker

GitHub Actions 会在 push 时自动构建并推送镜像到 GitHub Container Registry：

```text
ghcr.io/98624017/newapibackup:latest
ghcr.io/98624017/newapibackup:<branch>
ghcr.io/98624017/newapibackup:sha-<commit>
```

Zeabur 可以直接使用 `ghcr.io/98624017/newapibackup:latest` 部署。如果 GitHub Package 不是公开可拉取，需要在 GitHub Packages 页面把容器包可见性改为 public，或在 Zeabur 配置 GHCR 拉取凭证。

本地构建：

```bash
docker build -t newapi-backup-worker .
```

本地运行示例：

```bash
docker run --rm \
  -e DATABASE_URL="postgres://user:password@host:5432/newapi" \
  -e BACKUP_NAME="newapi" \
  -e BACKUP_INTERVAL_SECONDS="43200" \
  -e R2_ACCOUNT_ID="..." \
  -e R2_ACCESS_KEY_ID="..." \
  -e R2_SECRET_ACCESS_KEY="..." \
  -e R2_BUCKET_NAME="..." \
  -e R2_PREFIX="newapi" \
  newapi-backup-worker
```

## R2 路径

备份对象会写入：

```text
s3://<bucket>/<prefix>/full/YYYY/MM/<backup-name>-backup-YYYYMMDD-HHMMSS.sql.gz
s3://<bucket>/<prefix>/full/YYYY/MM/<backup-name>-backup-YYYYMMDD-HHMMSS.sql.gz.json
s3://<bucket>/<prefix>/full/latest.json
```

如果 `R2_PREFIX` 为空，则直接写入 bucket 根路径下的 `full/`。

## 恢复

下载并校验 gzip：

```bash
aws s3 cp "s3://<bucket>/<prefix>/full/YYYY/MM/<backup>.sql.gz" .
gzip -t "<backup>.sql.gz"
```

恢复到目标库：

```bash
gzip -dc "<backup>.sql.gz" | psql "$RESTORE_URL"
```

`pg_dump` 使用 plain SQL，并带有：

```text
--format=plain --no-owner --no-acl --clean --if-exists
```

生产恢复前建议先恢复到临时数据库，确认应用版本、扩展和数据完整性。

## 本地验证

```bash
timeout 60s go test ./...
timeout 60s docker build -t newapi-backup-worker:test .
```

## 项目结构

```text
cmd/backup-worker/main.go      容器入口
internal/backup/               配置、调度、dump、manifest、R2 上传
Dockerfile                     Zeabur 可部署镜像
```
