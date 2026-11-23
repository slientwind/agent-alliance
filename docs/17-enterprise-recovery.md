# 企业独立实例的备份与恢复

适用于本项目的 PostgreSQL 16、Temporal 默认命名空间和本地内容寻址对象卷。企业运维持有备份和密钥，业务人员仍从原项目或服务领取经审核的成果。恢复工具有两个用途：把快照还原到隔离环境做核对，或从备份取回当前正式库仍允许分发的交付原件。

## 准备

在 Compose 主机上设置三个绝对路径。归档目录和临时工作目录放在企业加密存储上，归档目录由企业复制到独立的备份位置。工作目录至少容得下整份解密后的备份；执行整库演练还需要对象和三份数据库的恢复空间。容器内 UID 10001 需要对两个目录有读写权限。密钥文件单独挂载为只读，只有运维身份可读取。以下路径为示例。

```sh
export RECOVERY_ARCHIVES=/srv/alliance-backup/archives
export RECOVERY_WORK=/srv/alliance-private/recovery-work
export RECOVERY_KEY_FILE=/srv/alliance-keys/backup-key
```

首次部署可用 `openssl rand -hex 32` 生成独立备份密钥，写入密钥管理系统或受保护文件。将备份密钥及其版本、业务资源加密密钥的恢复方法分别托管。运行配置、云资源授权、角色与数据库连接配置需由企业单独保管。备份工具只归档应用对象和三份数据库。复制归档时保留对应密钥版本。

在项目根目录构建运维镜像，并设置本机快捷命令：

```sh
ops() {
  docker compose --env-file .env.local -f deploy/compose.yaml \
    -f deploy/application.yaml -f deploy/recovery.yaml "$@"
}
ops build recovery
```

## 创建、校验与轮换

```sh
name="alliance-$(date -u +%Y%m%dT%H%M%SZ).age"
ops run --rm --no-deps recovery backup -archive "/archives/$name" -key-file /run/secrets/backup-key
ops run --rm --no-deps recovery verify -archive "/archives/$name" -key-file /run/secrets/backup-key
```

成功输出包含企业标识、各数据库快照、采样时间、观测到的 WAL 位置、对象数和过期时间。业务库的 pg_dump 和对象清单使用同一个导出快照，逐一核对原件大小和 SHA-256。三个数据库分别取得快照，恢复时逐项核对跨库状态。WAL 位置仅用于记录采样位置，连续时间点恢复还需独立的 WAL 归档。快照建立在可运行 pg_dump 的主库上。任一必需原件缺失或损坏都会使本次备份失败。已完成归档使用唯一文件名，重复执行保留原包。[PostgreSQL 16 备份说明](https://www.postgresql.org/docs/16/app-pgdump.html)

默认库名为 `alliance`、`temporal`、`temporal_visibility`。可使用 `-business`、`-temporal`、`-visibility` 指定。三个库在不同服务器时，挂载企业的 libpq service 文件，设置 `PGSERVICEFILE`，并通过 `-history-service` 和 `-visibility-service` 指定调度库的数据源。使用独立于其他企业的数据库和对象卷。

完整备份含个人偏好，默认保留 720 小时，可用 `backup -retain 168h` 等参数缩短。到期后该文件无法用于隔离还原或修复原件。企业可通过现有调度器按约定频率运行备份和校验，逐个归档执行下列命令。它按已认证清单的到期时间判断，返回 `removed` 及企业标识，未到期的归档保留。调度器应保留结果，遇到未找到密钥、校验失败或文件不可读时告警。所有物理副本、云对象历史版本、回收区和隔离演练目录的清理须分别配置和核验。

```sh
ops run --rm --no-deps recovery expire -archive "/archives/$name" -key-file /run/secrets/backup-key
```

## 在独立恢复环境暂存和核对

使用隔离网络和新的 PostgreSQL 16 实例，预装 pgvector 及原库使用的扩展，恢复身份具有创建数据库和扩展的权限。归档目录挂载为只读。在恢复主机配置目标库 `PGHOST`、`PGUSER`、只读 `PGPASSFILE`、可写 `TMPDIR` 和已加密的临时存储。下例假设企业已准备这些路径、镜像和名为 `alliance-drill` 的隔离网络。需要检验模型与 Agent 的加密凭据时，再挂载对应版本的业务资源密钥并传入 `-resource-key-file`。

```sh
docker run --rm --network alliance-drill --read-only --cap-drop ALL \
  -e PGHOST -e PGUSER -e PGPASSFILE=/run/secrets/pgpass -e TMPDIR=/work \
  -v "$RECOVERY_ARCHIVES:/archives:ro" -v "$RECOVERY_WORK:/work" \
  -v "$RECOVERY_KEY_FILE:/run/secrets/backup-key:ro" \
  -v "$DRILL_PGPASS_FILE:/run/secrets/pgpass:ro" \
  -v "$DRILL_RESOURCE_KEY_FILE:/run/secrets/resource-key:ro" \
  alliance-recovery:local stage -archive "/archives/$name" \
  -key-file /run/secrets/backup-key -resource-key-file /run/secrets/resource-key \
  -stage-dir "/work/drill-$name"
```

工具先完整验证密文、数据库备份摘要和对象内容，再向三个新数据库执行 pg_restore。报告保存在新目录的 `report.json`。其中包括：

- `Quarantined` 标识隔离状态。每个恢复库都有独立名称并默认只读，恢复业务库另有禁止 API、Worker 启动的标记。独立数据库管理员可以读取历史内容，恢复环境应限制访问并在演练后销毁。
- `Workflows` 以已派发的业务运行编号核对 Temporal 当前执行、执行 ID 和列表状态。报告区分未派发、找不到历史、列表缺项和状态不一致。超过 Temporal 历史保留期限的旧运行也可能缺项。运行正文、调用账本和队列均不会因核对而重新派发。
- `OpenRuns`、`UnresolvedCalls` 提示继续人工核对的范围。运维需区分已提交、结果未知及确有远端回执的调用，保留既有费用与重试次数。
- `ResourceKeys` 按原作用域检查模型密钥、Agent 独立密钥和候选密钥。报告区分未提供、无待核验凭据、全部匹配和存在不匹配，仅记录数量。实际账号授权和供应商可用性仍须独立验证。

断电或强制结束可能留下未完成的 `alliance_restore_*` 数据库和私有暂存文件。运维按本次演练环境清理。正式库整体丢失时，仍需从业务备份之外取得最新删除、撤权和在途调用依据，再决定开放范围。当前工具提供隔离恢复与核对，生产级整库切换、连续 WAL 恢复和旧 Worker 代次隔离仍待完成。此阶段保持隔离，勿直接将用户流量或 Temporal 服务指向旧副本。[PostgreSQL 16 恢复说明](https://www.postgresql.org/docs/16/app-pgrestore.html)

## 修复当前正式库中的交付原件

适用于正式业务数据库和当前权限记录可信、个别已交付文件丢失或损坏的情况。在原部署环境指定交付清单里的 `delivery` 批次编号：

```sh
ops run --rm --no-deps recovery repair-delivery \
  -archive "/archives/$name" -key-file /run/secrets/backup-key \
  -delivery "$DELIVERY_ID"
```

工具核对企业、当前批次的分发结论、固定版本、文件大小和摘要，与资料撤权及分发决定串行处理。有待核对或传播受限的批次会被拒绝。修复时只按正式库当前引用的摘要取回字节，使用原子替换，保留历史版本和审核记录。重复运行会核验已有文件。运维请求和最终核对结果写入审计记录，发生中断时可据原请求复查后重试。接收人仍通过原授权入口下载。已经撤销的应用、已清除的偏好、旧会话以及未知调用均不从旧库合并回来。

企业全域汇总导出也要核对当前授权。演练先备份一份可下载的企业报表，再收回导出权。隔离快照中仍有旧文件及当时的授权，正式库保留汇总查看权。用该快照修复获准的交付原件后，正式库的旧报表下载仍返回 404。[企业报表撤权恢复演练](evidence/rebuild-enterprise-summary/recovery.json)

资料清除后，旧快照仍可能有原件。演练从快照修复另一份获准交付时，同时核对已清除的资料在正式库中的引用、下载入口和对象文件。相同文件仍被独立交付引用时，获准版本可继续读取。[资料清除后的隔离恢复结果](evidence/rebuild-source-retention/recovery.json)

审核意见也要核验当前权限。演练在归档前用普通成员账号成功读取反馈，再撤销其依据资料并执行清除。从隔离归档修复另一份可交付文件后，同一成员仍可查看反馈状态，正文、定位引文及处理依据继续受限。旧归档中原有的可见范围仅用于核对。[审核意见恢复演练](evidence/rebuild-review-note-access/recovery.json)

飞书单聊绑定和待发通知同样取正式库当前状态。恢复演练保存一份包含旧绑定及一次历史发送尝试的快照，随后在网页解绑。用这份快照修复获准的交付原件后，隔离库可查原绑定，正式库保留解绑和提醒失效记录，发送尝试次数仍为一次。[单聊提醒恢复演练](evidence/rebuild-review-reminders/recovery.json)

## 复现演练

研发环境运行 `make smoke-recovery`。需要 Docker、Python 3、受支持的 Go 与 Node.js 工具链。演练使用独立命名的 PostgreSQL 和 Temporal 容器、新建合成企业、真实文件和 Chromium；网络需能取得镜像、前端依赖、浏览器及连接器依赖。测试结果写入 `artifacts/recovery-drill/<批次>/result.json`。小样本的还原耗时只反映本机条件。异地恢复、数据丢失窗口及正式服务恢复时长，要在企业实测数据量和设施上另行冻结。

恢复核对还包括账号状态、成员委托和通知回执。隔离快照可包含后来已降级的管理员和当时有效的委托。演练先在正式实例降级并停用该账号，然后从旧快照修复允许分发的交付文件，再核验旧会话、委托、角色及已读状态。[账号变更恢复实测](evidence/rebuild-member-profile/recovery.json)
