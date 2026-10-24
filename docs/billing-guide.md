# CLIProxyAPI 计费系统使用指南

> 开发者接手、架构、发布流程和历史踩坑请先阅读 [CLIProxyAPI Fork 开发交接手册](DEVELOPER_HANDOFF_CN.md)。

> v8.0.41 积分版本：升级前备份数据库并验证恢复，配置专用持久计费日志目录，核实管理员身份。历史余额保留 LEGACY 标记，需对账后才可启用积分收费。

## 一、部署配置

在 `.env` 或 Docker 环境变量中配置以下内容（按需启用）：

> 本地直接运行二进制时，工作目录的 `.env` 会自动加载。Docker Compose 默认只用宿主机 `.env` 做 `${VAR}` 插值，不会自动把所有变量传进容器；当前 `docker-compose.yml` 只显式转发了少数基础 Billing 变量。使用 SMTP、充值、Telegram、pricing、rate 等可选项时，请把它们逐项加入 `cli-proxy-api.environment`，或为该 service 添加 `env_file: [.env]`，然后重新创建容器。

```bash
# ===== 必填 =====
BILLING_ENABLED=true
BILLING_JWT_SECRET=<openssl rand -hex 32 生成>
BILLING_ADMIN_USER_ID=<已核实归属的用户 UUID> # 不再按未验证邮箱自动提权

# 计费专用数据库（billing 表自动创建）。这是 billing 自己的库，
# 与 PGSTORE_DSN 无关：只建 billing 的表，不接管 auth/config 存储——
# 你的文件式认证(auths/)和 config.yaml 照常工作，不受影响。
BILLING_DATABASE_URL=postgres://user:pass@host:5432/dbname
# 兼容：若未设 BILLING_DATABASE_URL 但设了 PGSTORE_DSN，billing 会复用那个库
# （但那种模式下 Postgres 会同时接管 auth/config 存储，文件不再被读取——不推荐）。

# ===== 积分与可靠结算 =====
BILLING_EVENT_DIR=/data/billing-events   # 必须挂载专用持久卷，单实例独占
# 兑换率、模型积分价格和助手配置在 Portal 管理页设置。
# 旧 BILLING_PRICING_FILE 必须清空；本版本拒绝把旧货币价格自动改作积分。

# ===== 限流 =====
BILLING_RATE_PER_SEC=5                    # 每用户每秒请求数（0=不限）
BILLING_RATE_BURST=20                     # 突发上限
BILLING_BALANCE_THRESHOLD=0               # 余额 ≤ 此值拒绝请求
BILLING_UNBILLED_BYPASS_RATE_LIMIT=false  # 免余额(unbilled)的 Key 是否也豁免限流（默认 false：仅豁免余额，仍受限流）

# ===== USDT 充值 =====
BILLING_USDT_TRC20=T...你的TRC20钱包地址
BILLING_USDT_AUTO_CONFIRM=false           # 共享地址无法证明付款归属；true 会阻止启动
BILLING_TRONGRID_API_KEY=<可选>
BILLING_USDT_POLL_INTERVAL=30s
BILLING_USDT_AMOUNT_TOLERANCE=0.005
BILLING_TOPUP_MIN_AMOUNT=10
BILLING_TOPUP_ORDER_TTL=24h

# ===== 微信/支付宝（个人收款码）=====
BILLING_WECHAT_QR_URL=https://your-cdn.com/wechat-qr.png
BILLING_WECHAT_NOTES=请按提示金额精确转账
BILLING_ALIPAY_QR_URL=https://your-cdn.com/alipay-qr.png
BILLING_ALIPAY_NOTES=请按提示金额精确转账

# ===== Telegram 通知（可选）=====
BILLING_TELEGRAM_BOT_TOKEN=123456:ABC...
BILLING_TELEGRAM_CHAT_ID=-100123456789

# ===== 邮箱验证码登录（可选，配了 SMTP 才启用）=====
# 用任意 SMTP 服务即可（Cloudflare 不提供对外 SMTP；免费可用 Gmail 应用专用密码 / Resend / Brevo）
BILLING_SMTP_HOST=smtp.gmail.com         # 设了才开启验证码登录；不设则前端隐藏该入口
BILLING_SMTP_PORT=587                     # 587=STARTTLS（默认），465=隐式 TLS
BILLING_SMTP_USERNAME=you@gmail.com
BILLING_SMTP_PASSWORD=<Gmail 应用专用密码>
BILLING_SMTP_FROM=you@gmail.com           # 不填默认用 USERNAME
BILLING_LOGIN_CODE_TTL=10m                # 验证码有效期（默认 10m）
BILLING_LOGIN_CODE_COOLDOWN=60s           # 同一邮箱两次请求最小间隔（默认 60s）
```

> 邮箱验证码登录：用户在登录页选「验证码」→ 填邮箱 → 收验证码 → 登录。**未注册的邮箱首次验证码登录即自动创建账号**。当前这类账号会写入随机密码，而修改密码接口仍要求原密码，因此暂时只能继续使用验证码登录，不能自行补设密码；这是待修问题。

### 管理员设置积分价格

打开 Portal → 管理 →「站点、积分兑换与配置助手」与「模型积分定价」。

- 分别填写 1 CNY、1 USDT 兑换的积分数。空值表示未开放该币种充值；没有默认报价。
- 每个模型设置输入、输出、缓存读取、缓存写入的每 100 万 token 积分单价。缓存价格 0 表示该项免费。
- 未定价或停用的模型拒绝付费调用。管理员保存后即时用于新请求，无需重启。
- 请求绑定开始时的价格版本；配置修改有版本冲突检查和数据库审计记录。
- 订单保存付款币种、实付金额、兑换率与到账积分。旧订单不会随兑换率变化重新计算。
- 历史货币余额和用量保留 LEGACY 标记，不自动换算，不计入新的积分统计。历史钱包对账完成前拒绝积分充值与扣款。

启用 billing 后，数据库、JWT、建表、持久事件目录或待结算事件恢复失败会阻止启动。已发布用量事件先同步写本机持久日志，再原子写入用量、积分扣款和流水。数据库故障后自动重放，同一事件 ID 只结算一次；不同 continuation 或子调用事件分别结算。

此机制不等于金额预留。并发请求和单次高消费仍需冻结预算，服务在生成用量事件前崩溃也需未完成请求对账，尚未完成这些上线条件。

---

## 二、用户使用指南

### 2.1 注册账号

打开浏览器访问：

```
http://你的服务器地址:端口/portal/ui/
```

首页展示模型积分价格、可核实的协议支持与文档入口。点击「开始使用」注册，填写邮箱和密码（至少 8 位），完成后自动登录。

### 2.2 创建 API Key

1. 左侧导航点击「API Keys」
2. 点击「创建新 Key」
3. **立即复制显示的 Key**（仅此一次显示，之后无法再查看明文）

Key 格式示例：`cpk_a1b2c3d4e5f6...`

### 2.3 使用 API Key 调用服务

和 OpenAI API 用法完全一致，只需把 base URL 换成代理地址，key 换成你的 key：

```bash
# OpenAI 兼容格式
curl http://你的服务器:端口/v1/chat/completions \
  -H "Authorization: Bearer cpk_你的key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "你好"}]
  }'
```

在各种客户端中使用：

```python
# Python (openai 库)
import openai
client = openai.OpenAI(
    api_key="cpk_你的key",
    base_url="http://你的服务器:端口/v1"
)
response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "你好"}]
)
```

```typescript
// Node.js
import OpenAI from 'openai';
const client = new OpenAI({
  apiKey: 'cpk_你的key',
  baseURL: 'http://你的服务器:端口/v1',
});
```

### 2.4 充值

1. 左侧导航点击「充值」
2. 选择支付方式（USDT / 微信 / 支付宝）
3. 输入充值金额，点击「创建订单」
4. 按提示操作：

**USDT-TRC20：**
- 复制显示的钱包地址
- 向该地址转入 **精确金额** 的 USDT
- 粘贴交易哈希（TX Hash）并提交
- 等待管理员核对链上实际到账与付款归属后，按订单快照增加积分

**微信/支付宝：**
- 扫描显示的收款二维码
- **务必按显示的精确金额转账**（例如 100.37 元，不是 100 元）
- 金额中的小数部分用于识别你的订单
- 管理员确认后到账

### 2.5 查看用量和账单

- **概览页**：当前余额、Key 数量、最近请求
- **用量页**：每条请求的详细信息（模型、token 数、费用）、30 天消费趋势图、模型分布

### 2.6 修改密码

左侧导航点击「设置」→ 输入当前密码和新密码 → 提交。

### 2.7 吊销 API Key

在「API Keys」页面点击对应 Key 的「吊销」按钮。吊销后该 Key 立即失效且不可恢复，需创建新 Key。

---

## 三、管理员使用指南

### 3.1 首次成为管理员

1. 先创建你自己控制的 Portal 账号，在预发布或维护环境核实账号归属。
2. 由运营方选定该账号的 UUID，设置 `BILLING_ADMIN_USER_ID`。
3. 清空旧的邮箱引导配置，按正常发布流程启动服务。
4. 仅所选 active 账号会被提升，已有管理员仍需人工核对。

仅配置旧 `BILLING_ADMIN_EMAIL` 会阻止启动，避免公共注册抢注目标邮箱后获得管理员权限。已有管理员权限保存在数据库中；已有部署可以移除旧的邮箱引导配置而不重新提升账号。

### 3.2 确认充值订单（微信/支付宝）

1. 点击「管理」→ 待确认充值列表
2. 打开你的微信/支付宝收款记录
3. 找到金额匹配的转账（例如 100.37 元）
4. 核对付款人、到账金额、时间、收款地址和订单归属后确认 → 按订单快照增加积分

USDT 同样需要人工核对付款归属。公开 TX Hash、金额相符或哈希未使用过，都不能单独证明付款属于该用户。

### 3.3 手动给用户充值/调整余额

在「管理」页面底部的「手动充值」区域：
1. 在用户列表中点击「选中」自动填入用户 ID
2. 输入金额（正数=充值，负数=扣减）
3. 填写备注（可选）
4. 点击「充值」

### 3.4 查看所有用户

「管理」页面显示所有注册用户及其当前余额。

### 3.5 Telegram 通知

配置 `BILLING_TELEGRAM_BOT_TOKEN` 和 `BILLING_TELEGRAM_CHAT_ID` 后，以下事件会推送通知：

| 事件 | 消息示例 |
|---|---|
| 新用户注册 | 🆕 新用户注册: user@example.com |
| 充值到账 | ✅ 充值确认: 用户 xxx, 金额 100.37 CNY |
| 管理员充值 | 💰 管理员充值: 用户 xxx, 金额 50.00 |
| 用户余额低 | ⚠️ 用户 xxx 余额不足: 0.50 |

### 3.6 定价调整

在「管理 → 模型积分定价」创建或编辑价格。模型名使用对客户公开的名称；输入、输出和两种缓存价格均为积分。保存后新请求生效，旧请求保留已绑定版本。

### 3.7 宣传首页与配置助手

管理员设置站点名称、公开 API Base（含 /v1）、文档 URL。文档 URL 留空时使用内置接入文档。首页不宣称未经测试的延迟、在线率或上游账号资质。

配置助手默认关闭。选择一个已定价模型并显式启用，运营方承担费用。默认每人每天 20 次、全站每天 500 次、最多输出 768 token，均可由管理员修改。每日次数以 UTC 计算，失败尝试也计数。助手接受最多 8 条文本和 12 KB 上下文，不执行工具；常见密钥格式会被替换为占位符，回答按纯文本展示。用户仍不应提交真实密钥。

付费请求要求 JSON 或模型 URL 明确指定模型。JSON 最大 8 MiB，不接受重复字段；当前付费入口拒绝无法在执行前绑定价格的 WebSocket 和 multipart 请求。

---

## 四、API 端点参考

所有端点前缀为 `/portal`。

### 公开端点

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /public | 公开站点、积分价格和兑换率 |
| GET | /config | Portal 可用登录方式等公开配置 |
| POST | /register | 注册 `{email, password, display_name}` |
| POST | /login | 登录 `{email, password}` → `{token, user}` |
| POST | /login/code/request | 请求邮箱登录验证码 |
| POST | /login/code/verify | 验证邮箱验证码并登录；未注册邮箱会创建账号 |

### 需要 Bearer Token

`POST /assistant` 接受 `{messages:[{role:"user",content:"配置问题"}]}`，返回 `{reply,model,sponsored:true}`。模型和限额由服务器决定，不能由客户端选择。

管理员接口新增 `GET/PUT /admin/settings` 与 `GET/PUT /admin/pricing`；保存时必须带读取到的版本号，过期版本返回 409。

注册、登录和 `/me` 返回的 `user.allowed_auth_ids` 为不透明账号标识；`/account-labels` 使用相同标识作为键。普通用户 `/usage` 的 `auth_id` 和 `/usage/filters` 的账号 ID 也使用该标识，筛选时应原样传回，不能传凭据文件名。管理员用量视图和 `/admin/users`、`/admin/accounts`、权限编辑接口继续使用原始调度 ID。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /me | 当前用户信息 |
| POST | /change-password | 修改密码 `{old_password, new_password}` |
| GET | /wallet | 余额 `{balance}` |
| GET | /usage?limit=50&before=RFC3339 | 用量记录（游标分页） |
| GET | /usage/filters | 当前用量筛选项 |
| GET | /usage/stats?days=30 | 聚合统计（日消费 + 模型分布） |
| GET | /api-keys | 列出所有 Key |
| POST | /api-keys | 创建 Key `{name}` → 含 `key` 明文 |
| DELETE | /api-keys/:id | 吊销 Key |
| GET | /account-labels | 当前用户可见的上游账号安全显示名 |
| GET | /quota | 当前用户允许账号的实时额度 |
| GET | /quota/:handle | 刷新单账号额度 |
| POST | /quota/:handle/reset | 重置单账号额度；要求管理员授予 `allow_reset` |
| GET | /topup/methods | 可用充值方式 |
| POST | /topup | 创建订单 `{amount, method, network}` |
| GET | /topup | 充值订单列表 |
| GET | /topup/:id | 订单详情 |
| POST | /topup/:id/submit | 提交 TX Hash `{tx_hash}` |
| POST | /topup/:id/cancel | 取消订单 |

### 管理员端点（需 is_admin=true）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /admin/users | 所有用户及余额 |
| GET | /admin/topup?user_id=&limit= | 所有充值订单 |
| POST | /admin/topup/:id/confirm | 确认充值 `{note}` |
| POST | /admin/credit | 手动调整余额 `{user_id, amount, note}` |
| POST | /admin/users/:id/limits | 设置某用户的访问控制 `{unbilled, allowed_models:[], allowed_auth_ids:[]}` |
| GET | /admin/models | 列出全部可选模型（供白名单选择） |
| GET | /admin/accounts | 列出全部上游账号（id/provider/label/status） |
| POST | /admin/accounts/alias | 设置客户可见账号别名 |

---

## 五、访问控制（仅超级管理员，按用户设置）

不再有"特权账号"这一层。**只有超级管理员**（`is_admin`，即用 `BILLING_ADMIN_EMAIL` 邮箱注册那个账号）能给**每个用户**设三样东西，其他账号完全没有管理入口：

| 字段（users 表） | 含义 |
|---|---|
| `unbilled` | **免余额**：跳过余额检查、用量仍记录但不扣钱包 |
| `allowed_models` | **可用模型白名单**：只能调用这些客户端可见模型（空 = 全部） |
| `allowed_auth_ids` | **可用账号白名单**：只能使用这些上游账号（空 = 全部） |

三者独立、任意组合，且**作用于该用户名下所有 API Key**（设置在用户上，不在 Key 上）。

> **当前重要限制：空列表表示“全部”，不是“禁止全部”。** 如果管理员把某用户允许的最后一个认证文件也删除，`allowed_auth_ids=[]` 会恢复为不限制账号。需要禁止用户请求时，应吊销该用户的全部 Key，或在后续版本实现明确的 `all / allowlist / none` 三态策略。不要直接把历史空值改成 deny-all，否则所有未配置白名单的现有用户都会被封禁。
>
> `users.status` 当前也没有参与 `cpk_` Key 的数据库认证。把用户标记为非 active 只会影响 Portal 登录，不能替代吊销 Key。

### 5.1 怎么设

管理员在「管理」页的用户列表点某用户的「**管理**」→ 弹窗里：

- 开关「**免余额**」；
- 选「**可用范围**」，两个选项卡可以单独使用，也可以组合使用（都不选 = 允许全部）：
  - **按模型选**：从全部模型里逐个勾（带搜索）；
  - **按认证文件选**：选一个/多个上游账号，等于放开这些账号提供的全部模型。

两类白名单都设置时，请求必须**同时满足**模型白名单和认证文件白名单。

对应 API：`POST /admin/users/:id/limits {"unbilled":true,"allowed_models":["gpt-4o"],"allowed_auth_ids":[]}`。

### 5.2 强制逻辑

- **免余额**：`unbilled` 用户的请求跳过 `BILLING_BALANCE_THRESHOLD`，计量时**不扣钱包**（用量照记）；默认仍受限流，除非 `BILLING_UNBILLED_BYPASS_RATE_LIMIT=true`。
- **限模型**：客户请求一个不在 `allowed_models` 里的模型 → 直接 **403**（`model not allowed for this account`）。HTTP、SSE、WebSocket、Home 和插件路由都会在实际执行前检查；`model(high)` 等 thinking 后缀按基础模型匹配。模型目录接口（OpenAI/Claude/Codex/Gemini/Home/Grok 格式）也只返回白名单内的模型，避免客户端展示无权调用的条目。
- **限账号**：把候选上游账号收窄到 `allowed_auth_ids`，再由 CLIProxy 自身的**轮询 / fill-first** 在其中调度（1 个=固定，多个=负载均衡）。传统选择器、新调度器快速路径和 Home 路由都应用同一白名单；无法映射到具体认证文件的插件执行器会拒绝受限请求。
- **配置校验**：提交不存在的模型或认证文件时，管理 API 返回 **400**，不会把无效选择静默保存成“无限制”。

> 说明：`allowed_auth_ids` 里的 ID 就是 `auths/` 下的**凭证文件名**（如 `gemini-user@x.com.json`）。改名/删除/换文件名会让该项失效——请求会因"无可用账号"失败。可选账号从 `GET /admin/accounts` 获取。
>
> 「按认证文件」限制的是**可被调度的凭证**。因此 Key 能调用的模型自然受这些凭证实际注册模型的限制；如果还要进一步缩小模型范围，可同时设置「按模型」。访问策略保存在用户上，该用户名下每个 Portal API Key 都继承同一策略。

### 5.3 审计日志

每条用量记录都写入实际服务它的上游账号（`auth_id`/`auth_label`），在「用量明细」的「账号」列可见；`unbilled` 请求还会打一条 `billing: unbilled request served`（含用户/账号/provider/model）。→ 谁用了哪个账号的额度可追溯。

---

### 5.4 请求日志与 commercial mode

`server.commercial-mode: true` 会在进程启动时完全跳过 `RequestLoggingMiddleware`，用于降低高并发时的内存和 I/O 开销。此模式下，即使运行时把 `observability.logs.request-log` 改为 `true`，也不能补装已经省略的 middleware，必须把 `commercial-mode` 设为 `false` 并重启进程后才会创建请求日志。

请求日志可能包含原始请求和响应内容。生产环境如无明确审计需求，建议保持 `commercial-mode: true` 与 `request-log: false`；如确需启用，必须先确认管理路由受 Cloudflare Access 或可信内网保护、日志目录权限已收紧，并设置清理上限。

---

## 六、常见问题

**Q: 余额显示为负数怎么办？**
A: 扣费发生在请求完成后（后付费），如果请求期间余额被耗尽，可能产生小额负值。充值后自动恢复。设置 `BILLING_BALANCE_THRESHOLD=0` 可以在余额为 0 时就阻止新请求。

**Q: 微信/支付宝充值金额有小数怎么回事？**
A: 为了区分不同用户的转账，系统自动在你输入的金额上加了 0.01-0.99 的尾数。请务必按显示的精确金额转账，否则管理员无法匹配到你的订单。

**Q: USDT 转账后多久到账？**
A: 启用自动确认后，通常 30 秒内。如果超过 5 分钟未到账，请确认 TX Hash 已正确提交，或联系管理员。

**Q: 可以同时创建多个 API Key 吗？**
A: 可以。每个 Key 的用量会分别记录但统一从同一个钱包扣费。

**Q: 忘记密码怎么办？**
A: 如果部署配置了 SMTP，可以继续用邮箱验证码登录，但当前还没有“验证码验证后重设密码”的流程；需要设置新密码时请联系管理员/开发者处理。未启用邮箱登录时也需联系管理员。
