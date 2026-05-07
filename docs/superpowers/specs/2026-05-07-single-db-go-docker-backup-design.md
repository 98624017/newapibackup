# Single-DB Go Docker Backup Design

## 背景

旧方案把备份能力拆成 Python worker、GitHub Actions fallback、多数据库配置和 R2 镜像，适合集中式备份，但对“每个 Zeabur 项目部署一个备份服务”的使用方式过重。

新的目标是：每个需要备份的项目都部署一个独立 Docker 服务。这个服务只备份当前项目的一个 PostgreSQL 数据库，直接上传到 Cloudflare R2。仓库不再保留 Python worker 和 AWS CLI 备份路径。

## 目标

1. 使用 Go 1.26 系列实现单数据库 backup worker。
2. 一个容器只备份一个数据库。
3. Docker 镜像运行时不依赖 Python、pip 或 AWS CLI。
4. 运行时只需要 `pg_dump`、Go 编译出的二进制和 CA 证书。
5. 通过环境变量配置数据库、R2 和调度间隔。
6. 支持 Zeabur 同项目部署，通过内网数据库地址降低外部网络依赖。
7. 每次成功备份后上传 `.sql.gz`、同名 manifest JSON 和 `full/latest.json`。

## 非目标

1. 不再支持一个 worker 同时备份多个数据库。
2. 不再保留 Python worker 兼容路径。
3. 不再依赖 GitHub Actions 执行数据库 dump。
4. 不实现 WAL/PITR、增量备份、R2 双写或跨账号镜像。
5. 不内置恢复执行逻辑；恢复仍由用户下载备份后用 `psql` 完成。

## 架构

```text
Zeabur project
  PostgreSQL
  backup-worker Docker service
    -> read env
    -> run pg_dump
    -> gzip stream to temp file
    -> calculate sha256 and size
    -> upload backup object to R2 through S3 API
    -> upload backup manifest
    -> upload full/latest.json
    -> sleep until next interval
```

Go 程序负责调度、配置校验、manifest、R2 S3 API 上传和退出信号处理。`pg_dump` 仍使用 PostgreSQL 官方 client，避免自己实现数据库 dump 协议。

## 配置

环境变量：

```text
DATABASE_URL              required, PostgreSQL connection URL
BACKUP_NAME               optional, default: backup
BACKUP_INTERVAL_SECONDS   optional, default: 43200
BACKUP_ON_START           optional, default: true
R2_ACCOUNT_ID             required
R2_ACCESS_KEY_ID          required
R2_SECRET_ACCESS_KEY      required
R2_BUCKET_NAME            required
R2_PREFIX                 optional, default: empty
BACKUP_STATE_DIR          optional, default: /tmp/backup-worker
```

`BACKUP_INTERVAL_SECONDS=43200` 表示默认每 12 小时备份一次。`BACKUP_ON_START=true` 表示容器启动后先执行一次备份，再进入循环。

## 对象路径

上传路径：

```text
s3://<bucket>/<prefix>/full/YYYY/MM/<backup-name>-backup-YYYYMMDD-HHMMSS.sql.gz
s3://<bucket>/<prefix>/full/YYYY/MM/<backup-name>-backup-YYYYMMDD-HHMMSS.sql.gz.json
s3://<bucket>/<prefix>/full/latest.json
```

`R2_PREFIX` 会自动去掉首尾 `/`，为空时直接写入 bucket 根路径下的 `full/`。

## Manifest

```json
{
  "schema_version": 1,
  "name": "newapi",
  "created_at": "2026-05-07T14:30:00Z",
  "object": "newapi/full/2026/05/newapi-backup-20260507-143000.sql.gz",
  "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "size": 19063111,
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

`full/latest.json` 的内容和 manifest 一致，表示最近一次已成功上传的备份。

## Docker

使用多阶段构建：

```text
builder: golang:1.26-alpine
runtime: alpine
```

runtime 安装：

```text
ca-certificates
postgresql-client
```

不安装 Python 和 AWS CLI。

## 错误处理

1. 缺少必填环境变量时启动失败。
2. `pg_dump` 失败时本轮备份失败，保留进程继续下一轮。
3. R2 上传失败时本轮备份失败，不更新 `latest.json`。
4. 临时文件在每轮结束后清理。
5. 收到 SIGINT/SIGTERM 后不启动新一轮；如果正在备份，等当前命令返回后退出。

## 验证

1. Go 单元测试覆盖配置解析、路径生成、manifest 和上传 key 组装。
2. 本地运行 `go test ./...`。
3. 构建 Docker 镜像验证 `docker build` 成功。
4. Zeabur 部署后先手动检查一次 R2 是否出现 `.sql.gz`、`.json` 和 `full/latest.json`。
5. 首次上线后至少做一次临时库恢复演练。
