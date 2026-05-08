# PostgreSQL R2 Backup Worker

轻量 PostgreSQL 备份服务，适合在每个 Zeabur 项目里单独部署一个 Docker 容器。一个容器只备份一个数据库，并把压缩后的 `.sql.gz` 上传到 Cloudflare R2。

## 适用场景

- 每个业务项目有一个 PostgreSQL 数据库需要定时备份。
- 希望备份服务和数据库部署在同一个 Zeabur 项目里，优先走内网数据库地址。
- 希望镜像轻量，不依赖 Python、pip 或 AWS CLI。
- 希望备份文件是普通 gzip 压缩 SQL，恢复方式简单。

不适合的场景：

- 一个容器同时备份多个数据库。
- WAL/PITR 秒级恢复。
- 增量备份或差分备份。
- 同时上传多个 R2 bucket。

## 工作方式

容器启动后会：

1. 读取环境变量。
2. 执行 `pg_dump`。
3. 用 gzip 压缩 dump 数据。
4. 上传 `.sql.gz` 到 R2。
5. 上传同名 manifest JSON。
6. 更新 `full/latest.json`。
7. 按 `BACKUP_INTERVAL_SECONDS` 进入下一轮。

默认启动后立即备份一次，之后每 12 小时备份一次。

## 镜像

GitHub Actions 会在 push 时自动构建并推送镜像到 GitHub Container Registry：

```text
ghcr.io/98624017/newapibackup:latest
ghcr.io/98624017/newapibackup:<branch>
ghcr.io/98624017/newapibackup:sha-<commit>
```

Zeabur 可以直接使用：

```text
ghcr.io/98624017/newapibackup:latest
```

如果 Zeabur 拉取失败，通常是 GHCR package 不是 public。到 GitHub 仓库的 Packages 页面，把 `newapibackup` 容器包可见性改成 public，或在 Zeabur 配置 GHCR 拉取凭证。

## Zeabur 部署

推荐部署方式：

1. 在你的业务 Zeabur 项目里新增一个服务。
2. 服务类型选择 Docker image / container image。
3. 镜像填写：

```text
ghcr.io/98624017/newapibackup:latest
```

4. 配置环境变量。
5. 启动服务。
6. 查看日志，确认出现 `backup uploaded object=...`。
7. 到 R2 检查 `full/latest.json` 和 `.sql.gz` 是否存在。

建议把备份服务和 PostgreSQL 放在同一个 Zeabur 项目里，并让 `DATABASE_URL` 使用 Zeabur 提供的内网连接地址。这样 `pg_dump` 不需要绕公网访问数据库。

## 环境变量

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

变量说明：

| 变量 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `DATABASE_URL` | 是 | 无 | PostgreSQL 连接串。生产建议使用 Zeabur 内网地址。 |
| `R2_ACCOUNT_ID` | 是 | 无 | Cloudflare account ID。 |
| `R2_ACCESS_KEY_ID` | 是 | 无 | R2 API token 的 access key。 |
| `R2_SECRET_ACCESS_KEY` | 是 | 无 | R2 API token 的 secret key。 |
| `R2_BUCKET_NAME` | 是 | 无 | R2 bucket 名。 |
| `BACKUP_NAME` | 否 | `backup` | 备份文件名前缀。建议用项目名，例如 `newapi`。 |
| `BACKUP_INTERVAL_SECONDS` | 否 | `43200` | 备份间隔，单位秒。`43200` 是 12 小时，`7200` 是 2 小时。 |
| `BACKUP_ON_START` | 否 | `true` | 容器启动后是否立即备份一次。 |
| `R2_PREFIX` | 否 | 空 | R2 对象前缀。建议用项目名隔离不同项目。 |
| `BACKUP_STATE_DIR` | 否 | `/tmp/backup-worker` | 临时文件目录。每轮结束后会清理。 |

## Cloudflare R2 权限

建议为备份服务创建单独的 R2 API token，不要复用个人全局 key。

最小权限建议：

```text
Object Read
Object Write
```

作用范围限制到目标 bucket。这个服务需要写入备份对象，也建议保留读权限，方便后续用同一组凭证做 smoke check 或下载验证。

## 备份路径

备份对象会写入：

```text
s3://<bucket>/<prefix>/full/YYYY/MM/<backup-name>-backup-YYYYMMDD-HHMMSS.sql.gz
s3://<bucket>/<prefix>/full/YYYY/MM/<backup-name>-backup-YYYYMMDD-HHMMSS.sql.gz.json
s3://<bucket>/<prefix>/full/latest.json
```

如果 `R2_PREFIX` 为空，则直接写入 bucket 根路径下的 `full/`。

示例：

```text
s3://xinbaoapi/newapi/full/2026/05/newapi-backup-20260508-003000.sql.gz
s3://xinbaoapi/newapi/full/2026/05/newapi-backup-20260508-003000.sql.gz.json
s3://xinbaoapi/newapi/full/latest.json
```

`latest.json` 指向最近一次成功上传的备份。只有备份文件和 manifest 都上传成功后，才会更新 `latest.json`。

## Manifest

同名 `.json` 和 `full/latest.json` 内容类似：

```json
{
  "schema_version": 1,
  "name": "newapi",
  "created_at": "2026-05-08T00:30:00Z",
  "object": "newapi/full/2026/05/newapi-backup-20260508-003000.sql.gz",
  "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "size": 16984003,
  "format": "plain sql gzip",
  "pg_dump": {
    "format": "plain",
    "no_owner": true,
    "no_acl": true,
    "clean": true,
    "if_exists": true
  }
}
```

可以用 `sha256` 和 `size` 校验下载后的备份文件。

## 本地运行

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

只想验证启动配置，可以先使用独立测试 prefix：

```text
R2_PREFIX=smoke/newapi-20260508
BACKUP_NAME=newapi-smoke
```

这样不会覆盖正式路径下的 `full/latest.json`。

## 首次上线检查

部署后看容器日志，成功时会出现：

```text
backup worker started name=newapi interval=12h0m0s prefix="newapi" bucket=<bucket>
backup uploaded object=newapi/full/YYYY/MM/newapi-backup-YYYYMMDD-HHMMSS.sql.gz size=<bytes> sha256=<hash>
```

然后检查 R2：

```bash
aws s3 cp \
  "s3://<bucket>/<prefix>/full/latest.json" \
  ./latest.json \
  --endpoint-url "https://<account-id>.r2.cloudflarestorage.com"
```

下载 `latest.json` 里的 `object`：

```bash
aws s3 cp \
  "s3://<bucket>/<object-from-latest-json>" \
  ./backup.sql.gz \
  --endpoint-url "https://<account-id>.r2.cloudflarestorage.com"
```

校验 gzip：

```bash
gzip -t backup.sql.gz
```

校验 sha256：

```bash
sha256sum backup.sql.gz
```

输出应等于 `latest.json` 里的 `sha256`。

## 恢复

生产恢复前，先恢复到临时数据库演练。

下载并校验 gzip：

```bash
aws s3 cp \
  "s3://<bucket>/<prefix>/full/YYYY/MM/<backup>.sql.gz" \
  ./backup.sql.gz \
  --endpoint-url "https://<account-id>.r2.cloudflarestorage.com"

gzip -t backup.sql.gz
```

恢复到目标库：

```bash
gzip -dc backup.sql.gz | psql "$RESTORE_URL"
```

`pg_dump` 使用 plain SQL，并带有：

```text
--format=plain --no-owner --no-acl --clean --if-exists
```

这意味着恢复脚本会尝试清理已存在对象。不要直接对生产库执行恢复，除非已经确认目标库就是要被覆盖。

## 调度建议

初始建议：

```text
BACKUP_INTERVAL_SECONDS=43200
```

也就是 12 小时一次。确认至少一次备份可恢复后，再按业务需要调高频率：

```text
BACKUP_INTERVAL_SECONDS=7200
```

也就是 2 小时一次。

## 常见问题

### Zeabur 拉不到 GHCR 镜像

现象：部署时报 unauthorized、not found 或 pull access denied。

影响：容器根本没有启动。

建议：

1. 到 GitHub Packages 页面确认 `ghcr.io/98624017/newapibackup` 是否存在。
2. 把 package visibility 改成 public。
3. 如果不能公开，在 Zeabur 配置 GHCR 用户名和 token。

### `DATABASE_URL` 连接失败

现象：日志里出现 `pg_dump failed` 或连接超时。

影响：不会产生新备份，也不会更新 `latest.json`。

建议：

1. 确认备份服务和 PostgreSQL 在同一个 Zeabur 项目或网络可达环境。
2. 优先使用 Zeabur 内网数据库地址。
3. 确认用户名、密码、库名正确。
4. 确认数据库允许该连接来源访问。

### R2 上传失败

现象：日志里出现 R2/S3 上传相关错误。

影响：本轮备份失败，不会更新 `latest.json`。

建议：

1. 检查 `R2_ACCOUNT_ID` 是否是 account ID，不是 bucket 名。
2. 检查 `R2_BUCKET_NAME` 是否正确。
3. 检查 access key 和 secret key 是否匹配。
4. 检查 token 是否有目标 bucket 的 Object Write 权限。

### 备份文件比预期小

现象：`.sql.gz` 只有十几 MB 或几十 MB。

影响：不一定是问题。PostgreSQL plain dump 经过 gzip 后压缩率通常很高。

建议：

1. 下载后执行 `gzip -t`。
2. 校验 sha256。
3. 恢复到临时数据库确认表和数据存在。

## 本地验证

```bash
timeout 60s go test ./...
timeout 60s docker build -t newapi-backup-worker:test .
timeout 60s docker run --rm -v "$PWD":/repo -w /repo rhysd/actionlint:latest .github/workflows/docker-publish.yml
```

## 项目结构

```text
cmd/backup-worker/main.go      容器入口
internal/backup/               配置、调度、dump、manifest、R2 上传
Dockerfile                     Zeabur 可部署镜像
.github/workflows/docker-publish.yml
                                GHCR 镜像发布 workflow
```
