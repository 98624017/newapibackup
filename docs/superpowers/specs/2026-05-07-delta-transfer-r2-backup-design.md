# Delta Transfer R2 Backup Design

## 背景

当前仓库通过 GitHub Actions 对多个 PostgreSQL 地址执行逻辑全量备份：

- `pg_dump "$DB_URL" --format=plain --no-owner --no-acl --clean --if-exists | gzip -9`
- 每天北京时间 12:00 和 00:00 运行
- 备份文件上传到 Cloudflare R2，支持 primary / secondary / both

业务量扩大后，一天两次备份的恢复点目标不够；但直接把全量备份提高到白天每 2 小时一次，会重复消耗服务器出口流量。服务器使用精品 CN2 线路，每月约 3T 流量，需要优先控制从服务器向外上传的数据量。

## 目标

1. 白天高频备份，目标为北京时间 08:00-20:00 每 2 小时一次，夜间保留一次合适时间备份。
2. 服务器只上传增量差分或必要的单份数据，避免对 primary / secondary 双桶重复上传。
3. R2 正式备份区最终保存完整备份文件，恢复时无需学习增量恢复流程。
4. 支持多个数据库地址，并允许每个数据库使用不同的 primary target 和 mirror target。
5. 失败时不污染正式备份，不破坏下一次差分基准。
6. 本地磁盘占用有固定上限，不随备份次数持续增长。

## 非目标

1. 第一阶段不引入 WAL 归档、WAL-G、pg_receivewal、PITR。
2. 第一阶段不修改正在运行的 PostgreSQL 镜像或容器配置。
3. 不在服务器端维护需要串联多个增量文件才能恢复的长链条。
4. 不要求 secondary R2 与 primary R2 实时一致；secondary 允许 2-6 小时延迟。

## 总体方案

采用“服务器差分上传，GitHub Actions 云端重建完整备份”的模型：

```text
服务器/Zeabur 侧
  1. pg_dump -Fc -Z0 生成本次完整 dump
  2. 用 xdelta3 基于上一份已发布成功的 full dump 生成 delta
  3. 用 zstd 压缩 delta
  4. 上传 delta 包和 manifest 到 primary R2 staging
  5. 如果 delta 太大，则上传 full dump 到 staging

GitHub Actions 侧
  6. 下载上一份正式 full dump 和本次 delta/full staging
  7. 重建本次 full dump
  8. 校验 sha256 和 pg_restore --list
  9. 校验通过后发布完整 .dump.zst 到 primary R2 正式区

secondary 同步
  10. 每 4 小时从 primary R2 正式区镜像到 secondary R2
```

增量只用于节省服务器上传流量。R2 正式区始终保存可直接恢复的完整备份文件。

## 备份格式

服务器端生成 dump 时使用：

```bash
pg_dump -Fc -Z0 --no-owner --no-acl --file="$PENDING_DUMP" "$DB_URL"
```

理由：

- `-Fc` 生成 PostgreSQL custom format，恢复时使用 `pg_restore`。
- `-Z0` 避免 pg_dump 内部压缩，提升二进制差分效果。
- 不使用 `gzip` 包裹原始 dump，因为压缩后的文件对 diff 不友好。
- 发布到正式区前再用 `zstd` 压缩完整 dump，减少 R2 存储和后续下载成本。

## 差分策略

每次差分只基于上一份“已发布成功”的完整 dump：

```text
base.dump    -> 上一份已发布成功的 full dump
pending.dump -> 本次新生成的 full dump
pending.xdelta -> base.dump 到 pending.dump 的差分
```

生成差分：

```bash
xdelta3 -e -s "$BASE_DUMP" "$PENDING_DUMP" "$DELTA_FILE"
zstd -T0 "$DELTA_FILE" -o "$DELTA_FILE.zst"
```

如果压缩后的 delta 过大，则自动降级为上传 full staging：

```text
delta_zst_size > pending_dump_size * 70% -> 上传 pending.dump.zst
```

这个阈值用于避免差分收益过低时仍承担重建复杂度。初始阈值为 70%，后续可根据一周实际数据调整到 65%-80%。

## 对象路径

每个数据库使用独立 prefix：

```text
<db-name>/
  full/
    <db-name>-backup-YYYYMMDD-HHMMSS.dump.zst
    latest.json
  staging/
    <run-id>/
      manifest.json
      delta.xdelta.zst
      full.dump.zst
  logs/
    <run-id>.json
```

说明：

- `full/` 是正式备份区，只放校验通过的完整备份。
- `staging/` 是中间区，可被定期清理。
- `latest.json` 记录当前最新正式备份，用作下一次差分基准。
- 恢复人员只需要从 `full/` 下载完整 `.dump.zst`。

## Manifest

服务器上传 staging 时必须同时上传 `manifest.json`：

```json
{
  "schema_version": 1,
  "db": "prod-a",
  "created_at": "2026-05-07T14:30:00+08:00",
  "mode": "delta",
  "base_object": "prod-a/full/prod-a-backup-20260507-123000.dump.zst",
  "base_archive_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "base_dump_sha256": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
  "delta_object": "prod-a/staging/<run-id>/delta.xdelta.zst",
  "delta_archive_sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "result_dump_sha256": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
  "result_size": 123456789,
  "dump_format": "pg_dump custom -Z0",
  "tools": {
    "pg_dump": "17.x",
    "xdelta3": "3.x",
    "zstd": "1.x"
  }
}
```

如果本次降级为 full staging：

```json
{
  "schema_version": 1,
  "db": "prod-a",
  "created_at": "2026-05-07T14:30:00+08:00",
  "mode": "full",
  "full_object": "prod-a/staging/<run-id>/full.dump.zst",
  "full_archive_sha256": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
  "result_dump_sha256": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
  "result_size": 123456789,
  "dump_format": "pg_dump custom -Z0"
}
```

校验字段语义：

- `*_archive_sha256`：校验 R2 上的压缩对象，例如 `.dump.zst` 或 `.xdelta.zst`。
- `*_dump_sha256`：校验解压后的 PostgreSQL custom dump 文件。
- `result_dump_sha256`：校验 GitHub Actions 重建出的未压缩 full dump。
- 正式发布后，`latest.json` 必须同时记录正式 `.dump.zst` 的 archive hash 和解压后 dump hash。

## GitHub Actions 重建与发布

新增或扩展 workflow 处理 staging 对象：

1. 读取 staging `manifest.json`。
2. 如果 `mode=delta`：
   - 下载 `base_object`。
   - 校验 `base_archive_sha256`。
   - 解压 base，并校验 `base_dump_sha256`。
   - 下载并解压 `delta.xdelta.zst`。
   - 校验 `delta_archive_sha256`。
   - 使用 `xdelta3 -d` 重建本次 full dump。
3. 如果 `mode=full`：
   - 下载并解压 `full.dump.zst`。
4. 校验重建结果：
   - `sha256` 必须等于 `result_dump_sha256`。
   - 文件大小必须等于 `result_size`。
   - `pg_restore --list` 必须能成功读取。
5. 校验通过后：
   - 发布前确认当前 `<db-name>/full/latest.json` 仍指向 manifest 的 `base_object`；如果已经变化，停止发布，避免旧任务覆盖新任务。
   - 将 full dump 用 `zstd` 压缩。
   - 上传到 `<db-name>/full/<filename>.dump.zst`。
   - 更新 `<db-name>/full/latest.json`。
   - 写入 `<db-name>/logs/<run-id>.json`。
6. 任何一步失败：
   - 不更新 `full/`。
   - 不更新 `latest.json`。
   - 保留 staging 供排查。
   - workflow 失败并告警。

每个数据库的发布 workflow 必须设置并发限制：同一 `db` 同一时间只允许一个重建/发布任务运行。不同数据库可以并行。

## 服务器本地状态

每个数据库只保留固定几类文件：

```text
state/<db-name>/
  base.dump
  pending.dump
  state.json
  tmp/
```

`state.json` 示例：

```json
{
  "db": "prod-a",
  "base_object": "prod-a/full/prod-a-backup-20260507-123000.dump.zst",
  "base_archive_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "base_dump_sha256": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
  "base_size": 123456789,
  "published_at": "2026-05-07T12:30:00+08:00"
}
```

状态推进规则：

1. 服务器生成 `pending.dump`。
2. 上传 staging。
3. GitHub Actions 发布成功后，服务器确认 `latest.json` 已更新到本次 `result_dump_sha256`。
4. 服务器将 `pending.dump` 提升为 `base.dump`，并更新 `state.json`。
5. 发布失败时不替换 `base.dump`，下一次仍基于上一份已发布成功的 full dump。

这个规则避免出现“服务器已切换基准，但 R2 正式区没有对应 full”的断链问题。

单库 in-flight 规则：

- 每个数据库同一时间只允许一个 staging 任务等待发布。
- 如果上一轮 staging 尚未发布成功，下一轮调度默认跳过该数据库并告警。
- 不排队堆积多个 pending dump，也不覆盖已有 `pending.dump`。

首次运行和本地基准丢失规则：

- 如果本地没有 `base.dump`，但 primary R2 存在 `<db-name>/full/latest.json`，服务器先下载该正式 full，校验 archive hash 和 dump hash 后作为 `base.dump`。
- 如果本地没有 `base.dump`，且 primary R2 不存在 `latest.json`，本次直接走 `mode=full` 的 full staging。
- 如果 `state.json` 损坏或与 R2 `latest.json` 不一致，以 R2 `latest.json` 为准；校验失败时停止备份并告警。

## 多数据库配置

长期配置应从旧的 `targets: primary|secondary|both` 升级为实例目标模型：

```yaml
backup_databases:
  - name: prod-a
    url_secret: PROD_A_DATABASE_URL
    schedule_group: daytime-2h
    primary_target: r2-primary-prod-a
    mirror_targets:
      - r2-secondary-prod-a
    delta:
      enabled: true
      max_delta_ratio: 0.70

  - name: prod-b
    url_secret: PROD_B_DATABASE_URL
    schedule_group: daytime-2h-offset-15m
    primary_target: r2-primary-prod-b
    mirror_targets: []
    delta:
      enabled: true
      max_delta_ratio: 0.70

r2_targets:
  r2-primary-prod-a:
    account_secret: R2_PRIMARY_ACCOUNT_ID
    access_key_secret: R2_PRIMARY_ACCESS_KEY_ID
    secret_key_secret: R2_PRIMARY_SECRET_ACCESS_KEY
    bucket_secret: R2_PRIMARY_BUCKET_NAME
    prefix: prod-a/

  r2-secondary-prod-a:
    account_secret: R2_SECONDARY_ACCOUNT_ID
    access_key_secret: R2_SECONDARY_ACCESS_KEY_ID
    secret_key_secret: R2_SECONDARY_SECRET_ACCESS_KEY
    bucket_secret: R2_SECONDARY_BUCKET_NAME
    prefix: prod-a/
```

语义变化：

- `primary_target` 是服务器上传目标。
- `mirror_targets` 是异步镜像目标。
- 服务器不会因为存在 mirror target 而上传第二份。

## 调度

初始建议：

```text
北京时间 08:00-20:00 每 2 小时一次
北京时间 02:30 夜间一次
```

多数据库串行错峰：

```text
08:00 prod-a
08:15 prod-b
10:00 prod-a
10:15 prod-b
后续时段按同样间隔继续到 20:00
02:30 prod-a
02:45 prod-b
```

理由：

- 降低同一时刻数据库 IO 压力。
- 降低服务器临时磁盘峰值。
- 降低 GitHub Actions 重建任务集中排队概率。

## Secondary 镜像

secondary 跨账号 R2 不由服务器上传。新增 GitHub Actions 镜像任务：

```text
每 4 小时：
  primary R2 full/ -> secondary R2 full/
```

镜像只处理正式区：

- 包括 `full/*.dump.zst`
- 包括 `full/latest.json`
- 可选包括 `logs/*.json`
- 不镜像 `staging/`

如果 secondary 落后超过 6 小时，则视为故障并告警。

## 保留策略

初始建议：

```text
primary full dump: 30 天
secondary full dump: 30 天
staging: 7 天或发布成功后 24 小时
logs: 90 天
服务器本地: 仅 base.dump、pending.dump、state.json
```

删除正式备份时不能删除最新 `base_object` 指向的文件。清理任务必须先读取各数据库 `latest.json` 和服务器 `state.json` 的基准引用。

## 恢复流程

恢复人员只需要使用正式 full 文件：

```bash
aws s3 cp "s3://<bucket>/<db>/full/<backup>.dump.zst" .
zstd -d "<backup>.dump.zst" -o "<backup>.dump"
pg_restore --clean --if-exists --no-owner --no-acl --dbname="$RESTORE_URL" "<backup>.dump"
```

不需要下载 delta，不需要手工拼接。

## 验证

上线前至少验证：

1. 单库首次 full staging 能发布正式 full。
2. 第二次 delta staging 能成功重建并发布正式 full。
3. 人为篡改 delta 后 workflow 失败，且不更新 `latest.json`。
4. delta 超过 70% 时能自动走 full staging。
5. `pg_restore --list` 能读取发布后的 full dump。
6. 从发布后的 `.dump.zst` 恢复到临时 PostgreSQL 成功。
7. 多库错峰不会互相覆盖本地 state 或 R2 prefix。
8. secondary 镜像只同步正式 full，不同步 staging。
9. 上一轮 staging 未发布完成时，下一轮调度会跳过该数据库并告警。
10. 本地 `base.dump` 丢失但 R2 `latest.json` 存在时，能下载并校验正式 full 作为新基准。
11. 旧的 GitHub Actions 发布任务不会覆盖较新的 `latest.json`。

长期验证：

- 每月或每次修改备份逻辑后，从 primary R2 的正式 full 恢复到临时 PostgreSQL，并执行基础 SQL 校验。
- secondary 至少每月抽样恢复一次，验证跨账号镜像结果可用。

## 风险与缓解

### 每次仍然完整 pg_dump

这个方案只节省服务器上传流量，不减少数据库导出压力。缓解方式：

- 多库错峰。
- 从白天 2 小时一次开始，不直接上 30 分钟。
- 监控每次 dump 耗时和数据库负载。

### 差分效果可能不稳定

如果 dump 输出变化过大，delta 可能接近 full。缓解方式：

- 使用 `pg_dump -Fc -Z0`。
- 设置 `max_delta_ratio=0.70` 自动降级。
- 记录每次 `delta_size / full_size`，一周后评估是否需要改用 directory format 或 restic/kopia。

### GitHub Actions 成为发布链路依赖

服务器上传 staging 后，正式 full 依赖 GitHub Actions 重建发布。缓解方式：

- staging 保留 7 天。
- workflow 失败告警。
- 服务器不切换 base，下一次仍可基于旧 full 重新生成 delta 或 full。
- 同一数据库发布 workflow 加并发限制，并在更新 `latest.json` 前校验当前基准仍匹配 manifest。

### 本地基准文件丢失或状态损坏

服务器本地只保留少量状态文件，因此需要能从 R2 正式区恢复基准。缓解方式：

- R2 `latest.json` 是权威基准。
- 本地 `state.json` 与 R2 不一致时，以 R2 为准重新下载正式 full。
- 重新下载后的 full 必须同时通过 archive hash、dump hash 和 `pg_restore --list` 校验。

### R2 正式区存储量仍按完整备份增长

这个方案牺牲 R2 存储来换取恢复简单和服务器流量节省。缓解方式：

- 正式 full 保留 30 天。
- secondary 只镜像正式 full。
- 后续如存储成本显著上升，再评估 restic/kopia 去重仓库。

## 后续升级选项

如果后续发现 pg_dump 压力大或需要分钟级 RPO，再评估：

1. 外部 backup-worker 使用 `pg_basebackup + pg_receivewal`。
2. WAL-G / pgBackRest 物理备份和 WAL 归档。
3. restic/kopia 对 dump 目录做内容去重存储。

这些不是第一阶段范围。
