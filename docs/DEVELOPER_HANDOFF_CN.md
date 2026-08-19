# CLIProxyAPI Fork 开发交接手册

> 本文面向接手 `ijpq/CLIProxyAPI` 的开发者，描述当前代码，而不是只描述上游项目。
> 基线：`claude/request-logs-billing` 分支，`fb71482b`（2026-08-08），文档整理日期 2026-08-19。
> 上游仓库：`router-for-me/CLIProxyAPI`；本 Fork：`ijpq/CLIProxyAPI`。

截至该基线，`origin/claude/request-logs-billing` 与本地 HEAD 一致；最后一次已验证交付为：

- Docker Hub：`ijpq/cli-proxy-api:claude-request-logs-billing`；
- 不可变 tag：`ijpq/cli-proxy-api:claude-request-logs-billing-fb71482`；
- manifest digest：`sha256:2646376c93192ff9af33bcbd8473c5a46d372453c2689a6cb564d88461a132b1`；
- GitHub Actions：[dockerhub-branch run 31252038177](https://github.com/ijpq/CLIProxyAPI/actions/runs/31252038177)，当时状态为成功。

这些是带日期的交接快照，不是永久的 latest；后续发布必须重新记录 commit、tag 和 digest。

## 1. 项目为什么存在

上游 CLIProxyAPI（下文简称 CPA）把多种 AI CLI/OAuth 账号统一暴露为 OpenAI、Gemini、Claude、Codex、Grok 兼容 API，并负责协议翻译、多账号调度、流式传输、WebSocket、配额和凭证生命周期。

本 Fork 的目标不是重写 CPA，而是在持续跟进上游的同时补足生产运营能力：

1. **商业 Portal 与计费**：用户注册、下游 API Key、钱包、计价、充值、限流、用量审计和管理后台。
2. **租户访问控制**：按 Portal 用户限制可调用模型和可使用的上游认证账号，同时保留 CPA 在允许范围内的轮询或 `fill-first` 调度。
3. **上游兼容与风控适配**：uTLS、Chrome HTTP/2、按凭证稳定的 User-Agent/客户端特征，降低异常客户端指纹带来的拦截风险。
4. **运维可见性**：请求日志浏览页、实际服务账号审计、实时配额、用量筛选和管理员全局视图。
5. **协议可靠性修补**：客户端取消向上游传播、Home 插件读取消、Codex Remote Compaction V2、xAI WebSocket 等。
6. **可交付镜像**：Fork 分支推送后自动发布 Docker Hub 的 amd64/arm64 镜像。

因此，本 Fork 是“CPA 上游能力 + 商业接入层 + 若干生产兼容补丁”。维护时应尽量保留这种叠加关系，避免复制或改写上游已有机制。

## 2. 先理解三个身份层级

| 概念 | 存放位置 | 用途 |
|---|---|---|
| Portal 用户 | Billing PostgreSQL 的 `users` | 钱包、费率、模型白名单、上游账号白名单、重置权限的归属主体 |
| Portal API Key | Billing PostgreSQL 的 `api_keys`，格式 `cpk_...` | 客户端调用 CPA 的下游凭证；同一用户的所有 Key 继承同一策略 |
| CPA 上游认证账号 | 默认在 `auths/`，运行时为 `coreauth.Auth` | CPA 真正拿去调用 OpenAI、Claude、Gemini、Grok 等上游的 OAuth/API 凭证 |

Portal/UI 是下游请求的第一层业务控制，但不是最终的上游执行器。它把一个用户可用的模型和认证账号范围写入请求上下文；CPA 的执行层再在这个范围内结合模型注册、冷却状态、优先级和调度策略选择真正的上游账号。

```text
客户端 + cpk_ Key
  -> CPA HTTP/WS 路由
  -> DB Key 认证（查出所属用户和访问策略）
  -> Portal 后置认证处理（限流、余额、模型/账号约束）
  -> CPA handler / Home / WebSocket
  -> CPA auth scheduler（仅在允许的账号集合中选择）
  -> provider executor + translator
  -> 上游模型
  -> usage meter（记录 token、费用、实际 auth_id/auth_label）
```

不要把“给 Key 绑定账号”理解为 `api_keys` 表上的绑定。当前实现是**按用户授权**：改变用户策略会同时影响该用户名下的全部 Key。

## 3. 仓库和运行架构

### 3.1 主要目录

| 路径 | 职责 |
|---|---|
| `cmd/server/` | 进程入口、命令行模式、存储初始化，以及本 Fork 的 Portal/Billing 装配 |
| `internal/api/` | Gin 服务、公开 API、管理 API、中间件和路由；包含本 Fork 的请求日志页 |
| `internal/api/modules/portal/` | Portal REST API、JWT 鉴权和嵌入式单页前端 |
| `internal/access/db_access/` | 用 PostgreSQL 中的 Portal Key 认证代理请求 |
| `internal/billing/` | 余额守卫、限流、计价、扣款、充值、邮件、Telegram、配额和上下文元数据 |
| `internal/store/` | 文件、PostgreSQL、Git、对象存储；Billing 表结构和 CRUD |
| `sdk/api/handlers/` | OpenAI、Claude、Gemini 等客户端协议入口和执行编排 |
| `sdk/cliproxy/auth/` | 上游认证账号管理、选择器、新调度器、Home 路由和执行重试 |
| `internal/runtime/executor/` | 各 provider 的真实网络执行器；只放执行器和单元测试 |
| `internal/runtime/executor/helps/` | 执行器共用的网络、uTLS、客户端特征等辅助代码 |
| `internal/chromeh2/` | 本 Fork 的 Chrome 风格 HTTP/2 层 |
| `internal/translator/` | 入站/出站协议翻译；变更需遵守 `AGENTS.md` 的额外约束 |
| `internal/thinking/` | thinking/reasoning 的统一规范化管线 |
| `internal/registry/` | 模型注册表和远程模型更新；`--local-model` 可禁用远程更新 |
| `internal/watcher/` | 配置和凭证热更新 |
| `internal/wsrelay/` | WebSocket relay 会话 |
| `internal/usage/` | CPA 原生用量处理；Portal 的持久化计费在 `internal/billing/` |
| `internal/managementasset/` | 下载和管理官方 Management Web UI 资源 |
| `sdk/cliproxy/` | 可嵌入其他 Go 程序的服务构建器和生命周期 |
| `test/` | 跨模块集成测试 |

### 3.2 普通请求管线

客户端路由在 `internal/api/` 中挂载，协议 handler 位于 `sdk/api/handlers/{openai,claude,gemini}`。handler 解析客户端可见模型，生成执行选项并交给 `sdk/cliproxy/auth.Manager`；Manager 根据注册模型、provider、账号状态、优先级和路由策略选择 Auth；provider executor 发出真实请求；translator 完成协议转换。

thinking 管线必须继续遵守：

```text
解析模型后缀 -> 生成 canonical ThinkingConfig -> 集中规范化/校验 -> provider-specific apply
```

即 `internal/thinking/` 中的“canonical representation → per-provider translation”架构。不要把 provider 特例绕过规范化后直接塞入 translator。

### 3.3 Management UI、请求日志页和 Portal UI 不是同一个东西

- 官方 Management UI 由 `internal/managementasset/` 从外部管理面板项目取得，不在本仓库直接维护。
- 本 Fork 的请求日志页面在 `internal/api/request_logs_page.go`，入口为 `/request-logs.html`；数据接口是 `/v0/management/request-logs` 和 `/v0/management/request-log-by-id/:id`，使用 Management Key。
- 商业 Portal 是本仓库内嵌 SPA，源码在 `internal/api/modules/portal/static/index.html`，入口为 `/portal/ui/`，使用 Portal JWT。

三者的鉴权和用途不同，排障时不要混用 Portal 登录 token、Management Key 和 `cpk_` API Key。

## 4. Portal、Key 和 CPA 的完整访问控制链路

### 4.1 数据模型

Billing 启动时由 `internal/store/billing_schema.go` 幂等创建或补齐以下表：

| 表 | 内容 |
|---|---|
| `users` | 用户、管理员标记、`unbilled`、`allowed_models`、`allowed_auth_ids`、`allow_reset` |
| `api_keys` | Key 哈希、前缀、名称、吊销时间和最后使用时间；不保存可再次读取的明文 |
| `wallets` | 用户当前余额 |
| `transactions` | 余额流水和充值幂等引用 |
| `usage_records` | 每请求 token、费用、状态、错误、实际 `auth_id`/`auth_label` |
| `topup_orders` | USDT、微信、支付宝充值订单 |
| `account_aliases` | 上游 Auth ID 到客户安全显示名的映射 |

`allowed_models` 和 `allowed_auth_ids` 目前以逗号分隔文本存储。空字符串解析为空切片，而**空切片的含义是不限**。

创建 Key 时，`internal/api/modules/portal/portal.go` 生成 `cpk_<64 hex>`；数据库只保存哈希和前 12 字符前缀，明文只在创建响应中出现一次。

### 4.2 每次请求如何执行策略

关键代码顺序如下：

1. `cmd/server/billing_wire.go:setupBilling` 注册 `db-api-key` access provider、余额守卫、限流、访问约束和计量插件。
2. `internal/access/db_access/provider.go` 从 `Authorization`、`X-Goog-Api-Key`、`X-Api-Key` 或兼容查询参数提取 Key。
3. `internal/store/billing_store.go:LookupAPIKey` 每次用 Key 哈希联表查询 `api_keys` 和 `users`；被吊销的 Key 不会命中。ACL 没有长期缓存。
4. 查出的 user ID、key ID、`unbilled`、允许模型和允许 Auth ID 写入 access metadata，再进入请求 context。
5. `cmd/server/billing_wire.go` 用 `handlers.WithAllowedModels` 和 `handlers.WithAllowedAuthIDs` 把限制送到执行上下文。
6. `internal/billing/enforce.go:ModelAccessGuard` 提前拒绝明显的模型越权；`sdk/cliproxy/auth/conductor_execution.go:validateAllowedModel` 按最终有效模型再次权威校验。`model(high)` 等 thinking 后缀按基础模型匹配。
7. `sdk/cliproxy/auth/scheduler.go:scheduledAuthPredicate`、传统选择路径和 `sdk/cliproxy/auth/conductor_home.go` 都会过滤不在允许集合中的 Auth。
8. 选中的真实 Auth ID 通过回调进入计量记录，最终写入 `usage_records.auth_id/auth_label`。

模型限制和账号限制是交集关系：

- 仅设模型：任意上游账号都可参与调度，但客户端只能请求白名单模型。
- 仅设账号：只能从这些凭证提供的模型和账号中调度。
- 两者都设：请求必须同时满足两者。
- 账号只设一个：效果近似固定到该账号。
- 账号设多个：CPA 在这个子集内继续 round-robin 或 `fill-first`。

无法证明自己实际用了哪个 Auth 的插件执行器，在存在账号白名单时会 fail closed，而不是绕过限制。

### 4.3 为什么这个功能以前没有真正生效

访问范围最初只接入了 CPA 的传统 Auth 选择链路。一次上游 rebase 引入/启用了内建 scheduler 快速路径，实际请求可能从快速路径直接选 Auth，从而绕过旧位置的 `allowed_auth_ids` 过滤；Home、WebSocket、count/stream 和插件路径也并非天然共享同一入口。

`fb71482b` 的修复不是只在 UI 上补校验，而是把约束贯穿到 handler metadata、内建 scheduler、传统选择器、Home、WebSocket、stream/count 和插件边界，并增加端到端相关测试。以后 rebase 如果上游再新增执行入口，必须确认限制是否传到了“真正选 Auth 的最后一层”。只检查 Portal API 返回值是不够的。

### 4.4 当前仍存在的关键语义风险：空列表等于全部

这是交接后应优先处理的设计债：

- 管理员从多个允许账号中删除一个，只要剩余列表非空，新请求就不能再选择被删除账号。
- 管理员把**最后一个**账号也删除后，保存的是 `[]`/空字符串；当前实现把它解释为“未设置限制”，即恢复为**全部账号可用**，而不是“一个都不许用”。

所以“移除某用户唯一的账号权限，但日志里仍看到他使用账号”可能完全符合现有实现。不能通过把空值全局改成 deny-all 来快速修复，否则所有历史上未配置 ACL 的用户都会被封禁。

建议做向后兼容的三态迁移：

```text
auth_access_mode = all       # 不限制
auth_access_mode = allowlist # 只允许 allowed_auth_ids
auth_access_mode = none      # 禁止所有上游账号
```

模型策略也可采用同样三态。数据库迁移时，现有空值应映射为 `all`；Portal UI 应明确提供“全部 / 所选 / 禁止”三个选择。

### 4.5 删除权限后仍有日志的排查顺序

1. 确认是不是删除了最后一个 ID；如果是，先按上述空值语义判断。
2. 确认请求使用的是 Portal 生成的 `cpk_` Key，而不是 `config.yaml` 中的静态 `api-keys`。后者不属于 Portal 用户，也不继承 ACL。
3. 确认 Key 属于被修改的 user ID；策略按用户生效，不按 Key 名称生效。
4. 从管理员接口 `/portal/admin/accounts` 获取并保存精确 Auth ID。Auth 文件改名会改变匹配关系；客户显示 alias 不是调度 ID。
5. 确认部署镜像包含 `fb71482b` 或更新提交，而非 Compose 默认镜像或旧分支标签。
6. 检查日志记录时间与请求开始时间。已建立的 SSE/WebSocket 请求保留开始时的 context，用量通常在完成后才写入；改 ACL 不会中途劫持已有连接。
7. 查 `usage_records.auth_id`，不要只看 provider/model 或客户 alias。

可用只读 SQL 辅助核对：

```sql
SELECT id, email, allowed_models, allowed_auth_ids
FROM users
WHERE email = 'customer@example.com';

SELECT k.id, k.key_prefix, k.revoked_at, u.email,
       u.allowed_models, u.allowed_auth_ids
FROM api_keys k
JOIN users u ON u.id = k.user_id
WHERE k.key_prefix = 'cpk_12345678';

SELECT created_at, request_id, provider, model, auth_id, auth_label, status
FROM usage_records
WHERE user_id = '<user UUID>'
ORDER BY created_at DESC
LIMIT 50;
```

## 5. Billing 和 Portal 业务行为

### 5.1 启动装配

`cmd/server/billing_wire.go` 只在 `BILLING_ENABLED=true` 时装配 Billing。还必须有：

- `BILLING_JWT_SECRET`；
- 推荐使用独立的 `BILLING_DATABASE_URL`；或者为兼容旧部署提供 `PGSTORE_DSN`。

缺少 secret、数据库或建表失败时，程序记录错误并禁用整个 Billing/Portal 装配。此时 Portal Key provider 也不会注册；`config.yaml` 的静态 Key 仍属于 CPA 的另一条认证链。

### 5.2 请求前和请求后

成功认证 Portal Key 后：

1. per-user rate limiter 检查速率和 burst；
2. balance guard 检查余额阈值；
3. 模型与 Auth 范围进入执行上下文；
4. 请求执行并得到 token usage；
5. meter 按 `provider/model` 或 `model` 定价，记录用量、实际 Auth 和错误状态；
6. 非 `unbilled` 用户扣钱包并写交易。

扣费是完成后的后付费，同一时间的并发请求可能使余额出现小额负数。余额检查有默认 10 秒缓存，充值/扣款路径应调用失效钩子；ACL 本身由每次 Key 查询取得，不共用这个余额缓存。

未配置 `BILLING_PRICING_FILE` 或没有命中模型价格时，用量仍会记录，但费用为 0。价格文件和 `BILLING_MARKUP` 在启动时加载，修改后需重启。

`unbilled` 只表示跳过余额检查和扣款，用量仍审计，默认仍受限流；只有 `BILLING_UNBILLED_BYPASS_RATE_LIMIT=true` 才同时绕过限流。

### 5.3 充值、登录和配额

- USDT、微信和支付宝都可作为展示/订单方式；当前链上自动确认 watcher 只实现 TRC20，依赖 `BILLING_USDT_TRC20` 和 TronGrid。
- 微信/支付宝个人收款码使用精确小数金额区分活跃订单，转账金额必须完全一致，最终由管理员确认。
- 配置 SMTP 后启用邮箱验证码登录；未注册邮箱首次验证码验证会创建用户。验证码只存在内存，重启会丢失未使用验证码。此类用户的数据库密码是随机值，而当前 `change-password` 又强制提交并校验旧密码，所以实际上还不能自行补设密码；代码注释和旧 UI 文案曾假设可以，这是当前待修问题。
- `BILLING_ADMIN_EMAIL` 只会在启动时提升已经存在的同邮箱用户。首次部署通常要先注册，再重启并重新登录。
- Portal 的“额度”页通过当前 Auth Manager 查询实时上游配额；账号展示给客户时使用 alias，避免泄漏凭证文件名或邮箱。
- Codex quota reset 还要求用户的 `allow_reset=true`，默认关闭。

完整用户、管理员和环境变量说明见 [计费系统使用指南](billing-guide.md)。

### 5.4 Portal 路由速查

所有路径以 `/portal` 开头：

- 公开：`/config`、`/register`、`/login`、`/login/code/request`、`/login/code/verify`。
- 用户：`/me`、`/change-password`、`/wallet`、`/usage`、`/usage/filters`、`/usage/stats`、`/api-keys`、`/account-labels`、`/quota`、`/topup`。
- 管理员：`/admin/users`、`/admin/credit`、`/admin/topup`、`/admin/users/:id/limits`、`/admin/models`、`/admin/accounts`、`/admin/accounts/alias`。

以 `internal/api/modules/portal/portal.go:RegisterRoutes` 为唯一权威来源；增加端点时同步更新 `docs/billing-guide.md`。

## 6. Fork 的其他关键改造

### 6.1 网络指纹

相关代码主要在 `internal/runtime/executor/helps/`、`internal/chromeh2/` 和 provider executor：

- OpenAI/Codex 路径使用 uTLS Chrome 特征；Codex WebSocket 也使用相应拨号逻辑。
- Google Code Assist 使用 Node TLS 规格；历史上曾短暂放入 uTLS host allowlist，随后因不兼容移除。
- User-Agent/客户端 profile 按凭证稳定，而不是每个请求随机变化。
- Chrome HTTP/2 层和 header order 在相关 host 之间复用。

这些逻辑不是一般意义上的“换一个 User-Agent”，TLS ClientHello、HTTP/2 参数和请求头也要相互一致。修改时必须用目标 provider 的真实路径测试，不能只用普通 `net/http` 单元测试推断结果。

### 6.2 请求日志浏览

`/request-logs.html` 会通过 Management API 列出日志并读取单个日志正文。管理 Key 存于浏览器 `sessionStorage`，关闭 tab 后消失，但页面仍可能展示原始请求/响应内容。生产环境必须把 Management 路由置于强认证和可信网络后，不要把日志页面当作客户功能公开。

### 6.3 取消传播

`10d93268` 和 `4bfd7133` 修复客户端断开后上游工作继续的问题，包括普通 streaming 和 Home 插件同步读。新增执行路径时必须传递原始 request context；不要为了“避免取消”换成 `context.Background()`。

遵守仓库约束：上游连接建立后不要添加新的网络超时。现有允许的超时例外列在 `AGENTS.md`。

### 6.4 Codex Remote Compaction V2

`b273a005` 保留并转发 Responses 请求中的 `context_management`，使 Remote Compaction V2 生效。该改动跨 handler、executor 和 translator；rebase 时若上游改变 Responses schema，需要用真实/录制 payload 检查字段没有在任何一层被删除。

## 7. 配置和部署

### 7.1 本地源码运行

```bash
cp config.example.yaml config.yaml
go run ./cmd/server --config config.yaml
```

常用参数：`--config`、`--tui`、`--standalone`、`--local-model`、`--no-browser`、`--oauth-callback-port`。`.env` 会从工作目录自动加载。默认凭证使用文件存储；也支持 PostgreSQL、Git 和对象存储。

Portal 最小配置：

```bash
BILLING_ENABLED=true
BILLING_JWT_SECRET=<openssl rand -hex 32>
BILLING_ADMIN_EMAIL=admin@example.com
BILLING_DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=disable
```

### 7.2 Docker Compose 的两种路径

从当前源码构建：

```bash
cp .env.example .env
# 填 BILLING_JWT_SECRET、BILLING_ADMIN_EMAIL
docker compose up -d --build
docker compose logs -f cli-proxy-api
```

运行 CI 已发布镜像：

```bash
CLI_PROXY_IMAGE=ijpq/cli-proxy-api:claude-request-logs-billing
docker compose pull cli-proxy-api
docker compose up -d
```

重要：`docker-compose.yml` 没有设置 `CLI_PROXY_IMAGE` 时默认使用 `eceasy/cli-proxy-api:latest`。它不保证包含本 Fork 的 Portal ACL 和补丁。排查“代码明明修了但线上仍旧”时，首先核对容器 image ID、tag 和构建 commit。

Compose 自带 `postgres:16-alpine`，数据库没有映射到宿主机端口，数据在 `pgdata` volume 中。`./config.yaml`、`./auths`、`./logs`、`./plugins` 分别挂载进容器。不要在升级或重建容器时误删 `pgdata` 和 `auths`。

注意 Docker Compose 对 `.env` 的语义：它默认只读取文件做 `${VAR}` 插值，**不会把所有变量自动注入容器**。当前 `docker-compose.yml` 只在 `environment:` 中显式转发了 Billing 开关、JWT、管理员邮箱、Billing DSN、markup、余额阈值和 unbilled 限流开关。SMTP、充值地址/watcher、Telegram、pricing path、rate 等如果只写进宿主机 `.env`，容器内看不到。需要在 `cli-proxy-api.environment` 中逐项映射，或显式添加：

```yaml
services:
  cli-proxy-api:
    env_file:
      - .env
```

加 `env_file` 后要重新创建容器，并用不回显 secret 的方式确认所需变量存在。不要用 `docker inspect`/日志把完整环境贴到工单或聊天中。

### 7.3 Billing 数据库和 CPA 存储必须区分

推荐始终设置 `BILLING_DATABASE_URL`：它只连接 Billing 库、创建 Billing 表，不接管 CPA 的认证和配置存储。

旧兼容方式只设置 `PGSTORE_DSN` 时，Billing 会复用该数据库，但 CPA 的 auth/config 存储也会切换到 PostgreSQL；此时磁盘 `auths/` 看似存在却不会按原方式生效。这是历史上真实踩过的坑，也是 `ed0955fe` 引入独立 Billing DSN 的原因。

## 8. 公网暴露和安全边界

Cloudflare 托管域名并用 Tunnel 连接服务器，只解决公网入口和源站连接，不自动替代应用鉴权。建议：

1. 源站 8317 不直接暴露公网，只允许 loopback、Docker 内网或 `cloudflared` 访问；同时在云安全组和主机防火墙关闭公开入口。
2. 对 `/portal/ui/` 和公开 API 使用 Cloudflare WAF、速率限制和滥用规则；管理路由优先放在 Cloudflare Access 后。
3. Management Key、Portal JWT secret、数据库口令和上游 OAuth 文件分离管理，禁止写进 Git 和日志。
4. `/v0/management/*`、`/request-logs.html`、OAuth 回调端口和数据库不应公开给普通客户。
5. 定期备份 Billing PostgreSQL、`auths/`、`config.yaml` 和定价文件，并演练恢复。

所谓“在 Nginx 加一层随机路径”，一般是把难猜的 URL 前缀 rewrite 到真实服务。它只能减少扫描噪声，属于 obscurity，不是身份认证；URL 可能从客户端配置、日志、Referer 或分享中泄漏。可以作为附加措施，但不能取代 Cloudflare Access、强 Key、WAF、限流和关闭源站端口。

## 9. 日常开发流程

### 9.1 开始前

```bash
git status --short
git branch --show-current
git remote -v
git fetch upstream
git fetch origin
```

当前惯例：

- `upstream` 指向 `router-for-me/CLIProxyAPI`；
- `origin` 指向 `ijpq/CLIProxyAPI`；
- 主要维护分支是 `claude/request-logs-billing`；
- 不要覆盖未确认归属的 dirty/untracked 文件。

先从问题对应的真实入口追踪到执行层，再改最小范围。Portal ACL 这类横切功能必须枚举 HTTP、SSE、WebSocket、count、Home、plugin 和 scheduler 快速路径，不能只改一个 handler。

### 9.2 修改和验证

Go 代码修改后：

```bash
gofmt -w .
go test -v -run TestName ./path/to/pkg
go test ./...
go build -o test-output ./cmd/server && rm test-output
git diff --check
```

最后一条 build 是仓库要求的必做项。提交前还应检查：

```bash
git diff --stat
git diff
git status --short
```

文档或配置改动不需要假装运行 Go 测试，但要检查链接、示例路径、YAML/JSON 语法和 `git diff --check`。

### 9.3 本机没有 Go 1.26 时

可以使用容器，缓存目录放在 `/tmp`：

```bash
docker run --rm \
  -v "$PWD:/app" \
  -v /tmp/cliproxy-go-mod-cache:/go/pkg/mod \
  -v /tmp/cliproxy-go-build-cache:/root/.cache/go-build \
  -w /app golang:1.26-bookworm \
  go test ./...

docker run --rm \
  -v "$PWD:/app" -w /app golang:1.26-bookworm \
  gofmt -w .
```

如果容器 build 报 `error obtaining VCS status`，优先把 `/app` 加入容器内 Git `safe.directory`，或在只验证编译时临时使用 `go build -buildvcs=false`。不要为绕过这个错误改仓库所有权。

### 9.4 提交原则

- 每个 commit 只表达一个可回滚意图；提交信息延续 Conventional Commit 风格。
- 不提交真实 `.env`、`auths/`、日志、Key、JWT secret 或数据库 dump。
- 不把辅助文件放进 `internal/runtime/executor/`；放到 `internal/runtime/executor/helps/`。
- 尽量不单独改 `internal/translator/`。如果任务只有 translator 改动，严格执行 `AGENTS.md` 的权限/Issue 规则。
- 新 Markdown 默认英文；中文专项文档以 `_CN.md` 命名。
- 使用 logrus 结构化日志，不记录 token/secret，不在 handler 中 panic 或 `log.Fatal`。

## 10. 从 upstream rebase 的标准流程

本 Fork 长期携带较多提交，rebase 是高风险日常操作。推荐流程：

```bash
git status --short
git fetch upstream
git fetch origin

# 用明确日期创建可恢复备份；不要移动/覆盖已有备份分支
git branch backup/request-logs-billing-pre-rebase-YYYY-MM-DD

git rebase upstream/main
```

若工作树有用户改动，先明确归属。确实需要暂存时使用带说明的 `git stash push -u`，并在 rebase 后核对、恢复；不要把未跟踪日志顺手提交。

冲突解决原则：

1. 先读 upstream 新实现，判断是否已有等价功能或新增执行入口。
2. 保留 Fork 的业务意图，不机械选择 `ours`/`theirs`。
3. 重点审计 `cmd/server/billing_wire.go`、`sdk/api/handlers/`、`sdk/cliproxy/auth/`、Home/WebSocket、provider executor 和 translator。
4. 解决每一批冲突后运行针对性测试；完成后运行全量测试和必做 build。
5. 用 `git range-diff` 或 `git log upstream/main..HEAD` 确认 Fork 提交没有丢失。

rebase 会重写 commit SHA。推送前先读取远端当前 SHA，只使用带**精确 lease** 的强推：

```bash
git fetch origin
git rev-parse origin/claude/request-logs-billing
git push --force-with-lease=refs/heads/claude/request-logs-billing:<刚确认的远端SHA> \
  origin HEAD:refs/heads/claude/request-logs-billing
```

不要用裸 `--force`。如果远端在 fetch 后又变化，lease 失败是保护机制，应重新检查，而不是绕过。rebase 后旧 GitHub workflow/commit 链接可能仍指向旧 SHA，这不代表新 commit 自动包含旧构建结果。

当前本地保留有多次 rebase 前的 `backup/request-logs-billing-*` 分支。清理前必须确认不再需要；它们是重写历史后的恢复点。

## 11. GitHub 和 Docker Hub 发布流程

### 11.1 GitHub

完成测试、build、diff 审核后正常 push；只有 rebase/重写历史才使用上节的 force-with-lease。推送后检查 workflow：

```bash
gh run list --branch claude/request-logs-billing --limit 10
gh run view <run-id> --log-failed
```

### 11.2 Docker Hub

`.github/workflows/dockerhub-branch.yml` 在以下分支 push 时运行：`main`、`dev`、`claude_update`、`claude/**`。需要仓库 secrets：

- `DOCKERHUB_USERNAME`
- `DOCKERHUB_TOKEN`

它通过 QEMU/buildx 发布 `linux/amd64` 和 `linux/arm64`，tag 规则是：

```text
<username>/cli-proxy-api:<branch-slug>
<username>/cli-proxy-api:<branch-slug>-<short-sha>
```

当前分支对应：

```text
ijpq/cli-proxy-api:claude-request-logs-billing
ijpq/cli-proxy-api:claude-request-logs-billing-<short-sha>
```

分支 tag 会被后续 push 覆盖，短 SHA tag 用于不可变部署和回滚。发布完成后验证：

```bash
docker buildx imagetools inspect \
  ijpq/cli-proxy-api:claude-request-logs-billing-<short-sha>
```

确认 manifest 同时包含 amd64/arm64，再更新生产。多架构 workflow 通常需要十几分钟；正在运行时不要再手工推同一 tag 制造竞态。

仓库还有上游 release 镜像 workflow 和一条旧的特定 uTLS 分支 GHCR workflow。日常 Fork 分支交付以 `dockerhub-branch.yml` 为准，不要因看到多个镜像 workflow 就假设它们发布到同一仓库。

## 12. Fork 历史记录

以下为相对当前 `upstream/main` 仍保留的主要演进。rebase 会改变 SHA，主题和日期比哈希更适合作为长期索引。

| 阶段 | 日期/代表提交 | 结果 |
|---|---|---|
| 分支镜像 CI | 2026-05，`17e00de1`、`73dd6d71` | push 分支自动构建 Docker Hub 多架构镜像，支持 `claude/**` |
| Codex/OpenAI 指纹 | 2026-05，`49d73aca`、`b10c44c0` | HTTP 和 WebSocket 引入 uTLS Chrome 特征 |
| 稳定客户端 profile | 2026-05，`a962a210`、`aee74cc0`、`ae346fc6` | 按凭证 UA；Google Code Assist 调整为 Node TLS，移出不兼容 allowlist |
| 运维日志页 | 2026-05，`857c1da8` | Management 下增加请求日志列表和正文浏览 |
| Chrome H2 与 xAI rebase 修复 | 2026-06，`c023fd39`、`472abbea`、`9feb3780` | 共享 Chrome 风格 H2、稳定头序和 WebSocket URL 修复 |
| Billing 基础 | 2026-05，`090487d5` 至 `3a167466` | Schema、DB Key、JWT Portal、钱包、计量、充值、限流、SPA、通知 |
| 用户 ACL | 2026-07，`ce9aff1d`、`bb1442be`、`f1f50cdd` | 从账号绑定演进为管理员按用户设置模型/Auth 白名单，拆分 `unbilled` |
| 自包含部署 | 2026-07，`580c462e`、`8591d1f4`、`ed0955fe` | Compose Postgres、发布镜像选择、独立 Billing DSN |
| Portal 运营能力 | 2026-07，`7a480b85` 至 `87b47417` | 邮件登录、alias、实时 quota/reset、管理员用量和权威账号审计 |
| 取消传播 | 2026-07，`10d93268`、`4bfd7133` | 普通 streaming 和 Home 插件在客户断开后取消上游工作 |
| 用量筛选 | 2026-07，`100d91bf` | 按模型、上游账号和用户过滤明细 |
| Codex 压缩 | 2026-07，`b273a005` | 转发 `context_management`，支持 Remote Compaction V2 |
| ACL 端到端修复 | 2026-08，`fb71482b` | 补齐 scheduler、Home、WS、stream/count 和插件边界的限制 |

查看完整当前序列：

```bash
git log --reverse --date=short --format='%h %ad %s' upstream/main..HEAD
```

## 13. 历史踩坑和故障模式

### 13.1 ACL 只在“第一关”校验不够

Portal middleware 能拒绝模型，但账号限制的最终权威点必须在 Auth scheduler。上游新增快速路径后，旧限制失效就是典型例子。任何新 handler、WebSocket 或插件执行入口，都要验证 metadata 是否一直传到最终选账号的位置。

### 13.2 空白名单并不表示 deny-all

当前空 `allowed_auth_ids`/`allowed_models` 表示 unrestricted。删到零项会重新放开，详见 4.4。这是现存风险，不是已修复历史。

### 13.3 共享 PGSTORE_DSN 意外接管认证存储

只为 Portal 配数据库却复用 `PGSTORE_DSN`，会让 CPA 从文件存储切到 Postgres，造成“auths 文件明明在但运行时账号不对”。现在优先使用 `BILLING_DATABASE_URL`。

### 13.4 Usage 丢 Key context 或选中账号不权威

异步/stream 路径曾丢失 Portal key context，管理页也曾不能可靠显示实际 Auth。`c5298e34`、`5a42de0a` 和 `87b47417` 分别修补。重构 context 或 goroutine 边界时，必须测试 user ID、key ID 和 selected auth callback 没有丢。

### 13.5 `prompt_cache_retention is not supported on this model`

这是所选上游模型拒绝请求字段产生的 HTTP 400，不是 Portal ACL 绕过。当前 Codex OAuth/xAI 相关执行器会删除不兼容的 `prompt_cache_retention`；仍出现时依次检查：

1. 是否运行旧镜像/错误镜像；
2. 是否走了另一个 OpenAI-compatible provider 或插件透传路径；
3. 客户端/模板是否持续注入该字段；
4. Portal 限制是否改变了实际 provider/Auth，使请求落到不支持此字段的模型。

最直接修复是从请求中移除该字段，或只对明确支持它的模型发送。不要在 Portal 权限代码里吞掉所有上游 400。

### 13.6 客户端断开后上游仍消耗额度

根因通常是执行路径改用了背景 context，或阻塞读取不监听 `ctx.Done()`。修复时不要用人为短超时终止已建立的上游连接，应正确传播取消。

### 13.7 指纹测试与真实 profile 不一致

截至 `fb71482b` 的一次 Go 1.26 全量测试中，`internal/runtime/executor` 有三项已知基线失败，断言期待 Linux 特征，而 Fork 实际返回 MacOS profile：

- `TestApplyClaudeHeaders_DisableDeviceProfileStabilization`
- `TestApplyClaudeHeaders_LegacyModePreservesConfiguredUserAgentOverrideForClaudeClients`
- `TestClaudeExecutor_NonClaudeRequestUsesClaudeCode220CLIFingerprint`

这不能永久当作“可忽略”。接手后应先在当前 upstream 基线重跑，确认是测试预期过时还是生产行为错误，再统一 profile 规范和测试。除此之外的新失败不能归因于这三项。

### 13.8 Compose 默认镜像不是 Fork

未设置 `CLI_PROXY_IMAGE` 时运行的是 `eceasy/cli-proxy-api:latest`。源码、GitHub commit 和线上容器可能因此不是同一版本。生产推荐短 SHA tag，并记录 manifest digest。

### 13.9 Auth ID、文件名和 alias 混淆

调度匹配真实 Auth ID，通常就是凭证文件 ID/文件名；alias 只用于客户显示。文件改名、删除或重新导入可能让历史白名单失效。管理界面必须以 `/admin/accounts` 返回的 ID 为准，日志同时保留真实 ID 和 label。

### 13.10 长连接让策略变更看似延迟

ACL 每次新请求查询数据库，但已经建立的 SSE/WebSocket 不会因为管理员改权限而自动断开；usage 又可能在连接结束时才落库。排查时同时比较请求开始时间、完成时间和部署时间。

### 13.11 GitHub Actions 警告不等于失败原因

历史 workflow 出现过 Node 运行时弃用警告，但构建仍成功。诊断失败应看 `gh run view --log-failed` 的首个真实 error，不要停在 warning。rebase 后 SHA 已变化，也不要用旧 workflow 状态判断新 commit。

### 13.12 日志和管理页面可能暴露敏感数据

原始请求日志可能含 prompt、请求体、provider 响应和账号线索。日志目录、Management API、浏览器会话和备份都按敏感数据处理；公开排障前先脱敏。

### 13.13 用户状态没有禁用已有 Portal Key

Portal 登录会检查 `users.status`，但 `internal/store/billing_store.go:LookupAPIKey` 当前只联表读取策略并检查 `api_keys.revoked_at`，没有要求用户为 active。因此手工把用户 status 改为 suspended 并不会阻止其已有 `cpk_` Key。实现正式的暂停用户功能前，运维必须吊销该用户全部 Key；代码修复还应增加数据库认证回归测试。

### 13.14 Compose `.env` 不会自动全部进入容器

宿主机 `.env` 能让 Compose 展开 `${BILLING_JWT_SECRET}`，不代表未列在 `environment:` 下的 `BILLING_SMTP_HOST`、`BILLING_PRICING_FILE` 等也会进入进程。历史排障中遇到“本地运行生效、容器运行不生效”时，必须比较容器实际环境与 Compose 映射；解决方式见 7.2。

## 14. 测试基线和回归矩阵

`fb71482b` 完成时，以下相关包测试和必做 build 已通过：

```bash
go test -count=1 \
  ./internal/access/db_access \
  ./sdk/cliproxy/auth \
  ./sdk/api/handlers \
  ./internal/billing \
  ./internal/api/modules/portal \
  ./internal/api \
  ./cmd/server

go build -o test-output ./cmd/server && rm test-output
```

全量 `go test ./...` 当时只有 13.7 中列出的三项已知失败。这个说明只用于比较历史基线，不替代当前重新测试。

ACL 相关变更至少覆盖：

| 场景 | 期望 |
|---|---|
| 非白名单模型 | HTTP 403，普通、stream、count、WebSocket 一致 |
| 单一允许 Auth | 实际 `usage_records.auth_id` 始终是该 ID |
| 多允许 Auth | 只在集合内按 CPA 策略轮转 |
| Auth 不提供请求模型 | 无可用账号错误，不回退到白名单外 |
| 内建 scheduler 快速路径 | 与传统 selector 一致应用白名单 |
| Home 路由 | 重派发也不能选白名单外账号 |
| 插件无法证明 Auth | 账号受限时 fail closed |
| Key 吊销 | 下一次请求立即认证失败 |
| 既有长连接 | 明确记录策略只在新请求生效 |
| 空列表 | 当前回归必须记录为 unrestricted，三态改造后再改变预期 |

Billing 相关还应测试余额临界值、并发后付费、`unbilled`、rate limit、价格未命中、充值幂等、订单过期、邮件验证码冷却/过期和管理员权限。

## 15. 常见故障排查

### Portal 页面不存在或 `cpk_` Key 全部无效

检查启动日志是否出现以下任一情况：Billing 未启用、缺少 `BILLING_JWT_SECRET`、没有数据库 DSN、数据库连接/建表失败。确认 `/portal` 路由挂载日志，并确认镜像是 Fork。

### Portal Key 返回余额不足或频繁 429

查 `wallets`、`transactions`、`BILLING_BALANCE_THRESHOLD`、`BILLING_RATE_PER_SEC` 和 burst。`unbilled` 默认并不绕过限流。余额缓存默认 10 秒，核对变更路径是否触发 invalidation。

### 用量有 token 但费用始终为 0

检查启动时是否提示没有 pricing file，随后检查价格键是否命中 `provider/model` 或 `model`，以及容器内是否真的挂载了该文件。修改价格后重启。

### 白名单模型仍返回无可用账号

模型白名单只允许客户端发起请求，不保证允许的 Auth 实际注册了该模型。查看 `/portal/admin/accounts`、模型 registry、Auth 状态/冷却和 provider 路由。两个白名单同时存在时取交集。

### Docker Hub workflow 挂掉

先看首个失败 step：登录失败通常是 secret/权限；build 失败按对应架构日志定位；manifest 缺架构检查 QEMU/buildx。修复后 push 新 commit 触发，不要只看旧 SHA 的红叉。

### 请求日志页 401/404

确认使用 Management Key 而不是 Portal JWT，确认 management route 配置允许远端访问，并使用 `/request-logs.html`。404 也可能表示运行的是不含 `857c1da8` 的镜像。

## 16. 当前待办与建议优先级

1. **P0：ACL 三态**。实现 `all / allowlist / none`，先做向后兼容迁移、UI 和全执行路径测试，解决“删掉最后一个账号反而放开全部”。
2. **P0：账号状态必须进入 Key 认证**。`LookupAPIKey` 当前只检查 `revoked_at`，没有检查 `users.status`；即使以后增加“暂停用户”管理功能，单改 status 也不会自动禁用已有 `cpk_` Key。修复前只能显式吊销其全部 Key。
3. **P1：修复邮箱首次登录后的密码设置/重置**。设计不要求旧密码的已验证邮箱重置流程，避免让随机密码账号永久只能用验证码。
4. **P1：把三个 fingerprint 基线失败变为明确通过**。先定义期望 profile，再改实现或断言，避免长期容忍红色全量测试。
5. **P1：部署可追溯性**。Portal/management 增加只读版本信息或在日志显著打印 commit，生产固定短 SHA tag + digest。
6. **P1：ACL 端到端集成测试**。用真实 HTTP/SSE/WS 入口、两套 Auth 和 usage DB 验证，而不仅是 auth manager 单测。
7. **P2：数据库迁移机制**。当前主要靠启动时 `CREATE/ALTER IF NOT EXISTS`；字段语义越来越复杂后应引入带版本的 migrations 和回滚/备份说明。
8. **P2：统一 Compose 环境变量装配**。明确选择逐项白名单或 `env_file`，让 `.env.example` 中承诺的 SMTP、充值、pricing、rate 等配置在默认 Compose 中真正可用，并加配置冒烟测试。
9. **P2：统一 Portal 文档和 OpenAPI**。`portal.go:RegisterRoutes` 仍是事实来源，现有手写端点表容易落后。
10. **P2：安全加固**。管理路由接入 Cloudflare Access，日志脱敏/保留策略，定期 secret 轮换和备份恢复演练。

## 17. 接手清单

### 第一日

- 取得 `origin`、`upstream`、GitHub Actions 和 Docker Hub 权限。
- 取得生产 `.env`、`config.yaml`、Billing DB、`auths/`、Cloudflare Tunnel/WAF 的安全交接，不要通过 Git 传 secret。
- 记录生产容器 image tag、digest、commit、数据库备份位置和当前告警渠道。
- 本地跑相关测试、全量测试和 build，确认历史基线是否仍成立。

### 第一次发布前

- 从 upstream rebase，并创建日期备份分支。
- 重点回归 ACL 的所有执行路径、计量实际 Auth、取消传播和 provider 指纹。
- 使用 force-with-lease 推送重写历史。
- 等待 Docker Hub 双架构 manifest 完成，使用短 SHA tag 部署。
- 冒烟测试登录、Key 创建、允许/拒绝模型、账号限制、usage、余额、管理日志和长连接。

### 每次事故后

- 记录用户 ID、Key prefix（不要记录明文）、request ID、请求开始/结束时间、model、provider、实际 Auth ID、镜像 digest 和 workflow SHA。
- 先判断问题发生在 Portal 认证/策略、CPA 调度、translator、executor、上游还是部署版本。
- 把可复现用例固化为最接近最终执行入口的回归测试，并更新本文“历史踩坑”。

## 18. 相关文档

- [Billing/Portal 部署与使用](billing-guide.md)
- [中国大陆网络优化](../CHINA_MAINLAND_NETWORK_OPTIMIZATION_CN.md)
- [SDK 使用](sdk-usage_CN.md)
- [SDK 高级扩展](sdk-advanced_CN.md)
- [SDK 认证](sdk-access_CN.md)
- [SDK 凭据 watcher](sdk-watcher_CN.md)
- 仓库级开发约束：`AGENTS.md`
- 上游用户手册：README 中的 `help.router-for.me` 链接

如果代码与本文不一致，以代码和测试为准，并在同一个变更中更新本文。
