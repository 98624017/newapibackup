# Zeabur Worker R2 Backup Design

Supersedes: `docs/superpowers/specs/2026-05-07-delta-transfer-r2-backup-design.md`

## 背景

原先按“全量压缩后约 250MB+”估算流量，因此设计了 xdelta 差分上传和 GitHub Actions 云端重建 full backup。后来从 R2 实际对象看到 `.sql.gz` 约 18MB，说明 PostgreSQL dump 压缩比很高。

当前 GitHub Actions 方案的主要问题不是 R2 对象大小，而是 `pg_dump` 在 GitHub Actions runner 上执行时，数据库服务器到 runner 的链路可能传输未压缩 dump 数据。更简单的优化是让备份在 Zeabur/服务器本地完成压缩，再只上传 `.sql.gz` 到 R2。

## 目标

1. 北京时间 08:00-20:00 每 2 小时备份一次，夜间 02:30 备份一次。
2. `pg_dump | gzip` 在 Zeabur 同项目或同服务器 backup-worker 中执行，通过内网连接 PostgreSQL。
3. 服务器只上传一份压缩后的 `.sql.gz` 到 primary R2。
4. secondary R2 由 GitHub Actions 从 primary R2 异步镜像，不消耗服务器出口上传两份。
5. 保持恢复方式简单：下载 `.sql.gz`，解压后用 `psql` 恢复。
6. 保留现有 GitHub Actions 全量备份作为临时兜底，等 worker 方案跑通后再停用或降频。

## 非目标

1. 第一阶段不实现 xdelta 差分上传和云端重建 full。
2. 第一阶段不引入 WAL、PITR、WAL-G、pg_receivewal。
3. 不依赖 Zeabur CLI 作为核心备份执行器。
4. 不要求 secondary R2 实时同步；允许最多 2-6 小时延迟。

## 总体方案

```text
Zeabur / 同服务器
  backup-worker
    ├── 内网连接 PostgreSQL
    ├── pg_dump --format=plain --no-owner --no-acl --clean --if-exists
    ├── gzip -9
    └── 上传 .sql.gz 到 primary R2

GitHub Actions
  ├── 每 4 小时 primary R2 -> secondary R2 镜像
  ├── 检查 primary R2 最近备份是否新鲜
  └── 暂时保留旧 backup-to-r2.yml 作为兜底

Zeabur 平台备份
  └── 开启每日自动备份，作为平台级兜底
```

## Backup-worker 职责

backup-worker 是一个独立服务或定时任务，不修改现有 PostgreSQL 镜像。

它负责：

1. 读取数据库配置。
2. 按数据库串行执行备份，避免多个库同时 dump。
3. 将 dump 流直接 gzip 到本地临时文件。
4. 计算 `.sql.gz` 的 `sha256` 和 size。
5. 上传到 primary R2。
6. 上传同目录下的 manifest JSON。
7. 删除本地临时文件。

命名规则：

```text
<db-name>/full/YYYY/MM/<db-name>-backup-YYYYMMDD-HHMMSS.sql.gz
<db-name>/full/YYYY/MM/<db-name>-backup-YYYYMMDD-HHMMSS.sql.gz.json
<db-name>/full/latest.json
```

manifest 示例：

```json
{
  "schema_version": 1,
  "db": "prod-a",
  "created_at": "2026-05-07T14:30:00+08:00",
  "object": "prod-a/full/2026/05/prod-a-backup-20260507-143000.sql.gz",
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

`latest.json` 指向最新成功上传并校验过的备份对象。

## 配置模型

保留 per-database 目标模型，但服务器只使用 `primary_target`：

```yaml
backup_databases:
  - name: prod-a
    url_env: PROD_A_DATABASE_URL
    primary_target: r2-primary-prod-a
    mirror_targets:
      - r2-secondary-prod-a
    schedule_offset_minutes: 0

  - name: prod-b
    url_env: PROD_B_DATABASE_URL
    primary_target: r2-primary-prod-b
    mirror_targets: []
    schedule_offset_minutes: 15

r2_targets:
  r2-primary-prod-a:
    account_env: R2_PRIMARY_ACCOUNT_ID
    access_key_env: R2_PRIMARY_ACCESS_KEY_ID
    secret_key_env: R2_PRIMARY_SECRET_ACCESS_KEY
    bucket_env: R2_PRIMARY_BUCKET_NAME
    prefix: prod-a/

  r2-secondary-prod-a:
    account_env: R2_SECONDARY_ACCOUNT_ID
    access_key_env: R2_SECONDARY_ACCESS_KEY_ID
    secret_key_env: R2_SECONDARY_SECRET_ACCESS_KEY
    bucket_env: R2_SECONDARY_BUCKET_NAME
    prefix: prod-a/
```

配置可以由本地 YAML 生成 GitHub Actions secret，也可以直接作为 JSON env 提供给 backup-worker。

## 调度

建议初始调度：

```text
08:00, 10:00, 12:00, 14:00, 16:00, 18:00, 20:00
02:30
```

多数据库串行错峰：

```text
08:00 prod-a
08:15 prod-b
10:00 prod-a
10:15 prod-b
后续白天时段按同样 15 分钟错峰继续到 20:00
02:30 prod-a
02:45 prod-b
```

Zeabur CLI 可用于部署、查看日志、重启 backup-worker，但不作为备份逻辑本身的依赖。

## Secondary 镜像

GitHub Actions 每 4 小时运行一次：

```text
primary R2 full/*.sql.gz + *.json + latest.json
  -> secondary R2 对应 prefix
```

规则：

- 只镜像正式 `full/` 下的对象。
- 如果 secondary 已存在同 key 且 size 一致，则跳过。
- 如果 `latest.json` 落后超过 6 小时，workflow 失败并告警。

## 验证

上线前验证：

1. backup-worker 对单库生成 `.sql.gz`。
2. 上传后 R2 对象 size 和 manifest size 一致。
3. `sha256` 校验一致。
4. 下载 `.sql.gz`，`gzip -t` 通过。
5. 解压后能恢复到临时 PostgreSQL。
6. 多数据库错峰不会互相覆盖对象路径。
7. secondary 镜像只复制 `full/` 正式对象。

长期验证：

- 每月从 primary R2 抽样恢复一次。
- 每月从 secondary R2 抽样恢复一次。
- 每次调整备份脚本或调度后执行恢复演练。

## 风险与缓解

### 每次仍然完整 pg_dump

这个方案降低服务器出口流量，但不降低数据库 dump 压力。

缓解：

- 多数据库错峰。
- 先按 2 小时频率上线，不直接上 30 分钟。
- 记录每次 dump 耗时、压缩后大小和上传耗时。

### backup-worker 自身故障

如果 worker 挂了，高频备份会停止。

缓解：

- GitHub Actions 健康检查 primary R2 最新备份时间。
- Zeabur 监控 worker 日志和重启状态。
- 临时保留旧 GitHub Actions 备份兜底。

### R2 secondary 延迟

secondary 是异步镜像，可能落后。

缓解：

- 允许 2-6 小时延迟。
- 超过 6 小时报警。
- primary R2 是恢复优先来源，secondary 是灾备兜底。

## 后续升级选项

如果后续压缩后全量明显变大，或 pg_dump 压力明显升高，再考虑：

1. xdelta 差分上传和云端重建 full。
2. restic/kopia 去重仓库。
3. WAL-G / pgBackRest / PITR。

这些不是第一阶段范围。
