# 部署、迁移与恢复

## 开发环境

需要 Go 1.24.4+、Node.js 22+、Docker Compose。项目自带的 `.tools/` 是当前工作区的本地工具缓存，不上传 GitHub；脚本优先使用该目录，不存在时使用 PATH。

```bash
npm --prefix frontend ci
./scripts/dev.sh
```

浏览器访问 `http://127.0.0.1:5180`。API 为 8180、gRPC 为 8181；两个 worker 独立运行，PostgreSQL 为本机 55432。Mock 远端为 8090。演示入口只在显式开发模式和 loopback 请求下启用。开发脚本中的数据库密码、master key 均为公开测试夹具，不能用于生产。

本地 Langfuse：

```bash
docker compose -p agent-alliance-observe -f deploy/langfuse.local.yaml up -d
```

页面 `http://localhost:13100`，测试账号 `alliance@example.test` / `alliance-local-password`；项目 ID `alliance-local-project`，公钥 `pk-lf-alliance-local`，私钥 `sk-lf-alliance-local`。这些是公开的本地测试值。容器与其他项目隔离，只有 Web 端口绑定本机。镜像使用 v4 系列，本次实测版本 v4.80.0；上线应固定经过验证的镜像 digest。

## 生产部署

复制 `deploy/.env.example` 到不提交的私有 env 文件，分别生成数据库密码和 AES 主密钥。主密钥必须是 base64 编码的 32 个随机字节，保存在密钥管理系统并做离线备份；丢失后无法恢复 Agent/Langfuse/回调凭据。

```bash
openssl rand -base64 32
openssl rand -hex 24
docker compose --env-file /secure/alliance.env -p agent-alliance-prod -f deploy/production.yaml up -d --build
```

部署包含 Go API（同时提供构建后的 React 页面）、两个 Go worker 和 PostgreSQL。API/worker 镜像由多阶段构建产生，非 root 用户、只读文件系统、去除 Linux capabilities。数据库不映射到主机端口；HTTP/gRPC 只绑定 loopback，交给宿主 ingress 转发。

在 ingress 为 HTTP 和 gRPC 配置 TLS，保留 Host，允许 SSE 长连接，禁用 SSE 缓冲并设置大于最长任务的超时。gRPC upstream 使用 Compose 私有网络中的 h2c；跨主机部署应使用 `ALLIANCE_TLS_CERT`、`ALLIANCE_TLS_KEY` 直接启用 gRPC TLS。公网 URL 配置必须与入口地址一致。不要在公网使用 `ALLIANCE_INSECURE_COOKIES=true` 或开放明文 gRPC。

公开注册默认关闭。初始化管理员时，可临时启用注册，仅在受限网络中创建第一个账号，再关闭注册并重启 API。后续成员必须先有账号；组织级账号开通与 SSO 接入需按部署组织的身份政策补充。

`ALLIANCE_ALLOW_PRIVATE=false` 默认防止向内网发起请求。如果企业模型位于私网，需要在有 egress 防火墙/域名白名单的网络中显式启用；当前不是逐 Agent 域名白名单产品。

## 数据迁移

PostgreSQL 初始化和增量建表在 advisory lock 下运行；已有工作区数据不清空。不同主密钥启动会通过加密哨兵校验失败，防止以错误密钥继续写入。

旧 JSON 迁移只在显式 demo workspace 导入模式启用：`ALLIANCE_IMPORT_JSON=/path/state.json`。源文件只读；仅允许导入空工作区，保存已导入标记；旧 provider key 复制为每个引用 Agent 的独立密文后移除 provider key。未终态旧运行标记失败，避免假装跨存储恢复不存在的进程。使用生产组织数据迁移时应先在独立环境演练并核对数据。

## 备份与恢复

```bash
./scripts/backup.sh agent-alliance-prod-postgres-1 /secure/backup-YYYYMMDD.dump
./scripts/restore.sh agent-alliance-prod-postgres-1 alliance_restored /secure/backup-YYYYMMDD.dump
```

备份为 PostgreSQL custom 格式，文件权限 600；包含加密凭据和账号哈希，仍需加密存储并限制访问。恢复脚本只创建新数据库，目的数据库已存在会失败，不覆盖在线数据库。AES 主密钥独立备份，不能依赖数据库 dump 找回。

恢复后先停止旧 worker，启动指向新数据库的 API 和 worker，确认主密钥校验、账号/Agent 数量、服务发布版本和新建 Mock 运行。旧队列任务的绝对 deadline 不重置；过期任务由清扫器终结。确认后再切换 ingress。需要时间点恢复时配置 PostgreSQL WAL 归档/PITR，而不只依赖定期 dump。

Langfuse 有自己的 PostgreSQL/ClickHouse/对象存储/Redis 数据，独立备份；平台数据库中的 outbox 不是 Langfuse 全量备份。已发送记录不自动重放，避免重复观测。

## 升级与运行边界

- 同版本 API 和 worker 可以多副本运行，任务领取与写入均有数据库隔离；滚动升级需保持 payload/schema 向后兼容。
- 数据库中断期间不接受成功的写入；worker 失去租约后不能提交输出。恢复后接管仍受固定截止时间约束。
- 工作区 JSONB 会随运行历史增长，当前没有自动清理历史、归档、跨区域灾备或生产负载 SLO 认证。上线前按业务规模补充容量验证和保留政策。
- 生产应关闭演示模式和不受控注册，配置网络入口/出口、日志采集、数据库备份告警和容器资源配额。
- `GET /healthz` 验证 PostgreSQL 连通性；任务事件和导出状态用于故障排查。外部执行和观测传输不提供跨系统 exactly-once。
