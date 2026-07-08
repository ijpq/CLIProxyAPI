# CLIProxyAPI 计费系统使用指南

## 一、部署配置

在 `.env` 或 Docker 环境变量中配置以下内容（按需启用）：

```bash
# ===== 必填 =====
BILLING_ENABLED=true
BILLING_JWT_SECRET=<openssl rand -hex 32 生成>
BILLING_ADMIN_EMAIL=you@example.com      # 你的注册邮箱，启动后自动升为管理员

# Postgres（已有，billing 表会自动创建）
PGSTORE_DSN=postgres://user:pass@host:5432/dbname

# ===== 定价 =====
BILLING_PRICING_FILE=/data/pricing.json  # 模型定价表路径
BILLING_MARKUP=1.20                       # 在上游成本上加价 20%

# ===== 限流 =====
BILLING_RATE_PER_SEC=5                    # 每用户每秒请求数（0=不限）
BILLING_RATE_BURST=20                     # 突发上限
BILLING_BALANCE_THRESHOLD=0               # 余额 ≤ 此值拒绝请求
BILLING_UNBILLED_BYPASS_RATE_LIMIT=false  # 免余额(unbilled)的 Key 是否也豁免限流（默认 false：仅豁免余额，仍受限流）

# ===== USDT 充值 =====
BILLING_USDT_TRC20=T...你的TRC20钱包地址
BILLING_USDT_AUTO_CONFIRM=true
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
```

### pricing.json 示例

```json
{
  "openai/gpt-4o": {
    "input_per_million": 2.50,
    "output_per_million": 10.00,
    "cache_read_per_million": 1.25,
    "cache_write_per_million": 3.75
  },
  "openai/gpt-4.1-mini": {
    "input_per_million": 0.40,
    "output_per_million": 1.60
  },
  "anthropic/claude-sonnet-4-5-20250514": {
    "input_per_million": 3.00,
    "output_per_million": 15.00,
    "cache_read_per_million": 0.30,
    "cache_write_per_million": 3.75
  },
  "google/gemini-2.5-pro": {
    "input_per_million": 1.25,
    "output_per_million": 10.00
  }
}
```

定价键格式为 `provider/model`（优先匹配）或 `model`（回退匹配）。未匹配的模型用量仍会记录，但费用为 0。

---

## 二、用户使用指南

### 2.1 注册账号

打开浏览器访问：

```
http://你的服务器地址:端口/portal/ui/
```

点击「注册」，填写邮箱、密码（至少 8 位），完成注册后自动登录。

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
- 链上确认后自动到账（约 30 秒）

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

1. 在环境变量中设置 `BILLING_ADMIN_EMAIL=你的邮箱`
2. 在 Portal 页面用该邮箱注册
3. 重启服务（或下次启动时会自动提升）
4. 重新登录后左侧导航会出现「管理」入口

### 3.2 确认充值订单（微信/支付宝）

1. 点击「管理」→ 待确认充值列表
2. 打开你的微信/支付宝收款记录
3. 找到金额匹配的转账（例如 100.37 元）
4. 确认无误后点击「确认」→ 用户余额立即增加

USDT 充值在链上自动确认，无需手动操作。

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

编辑 `BILLING_PRICING_FILE` 指向的 JSON 文件，重启服务生效。

加价率 `BILLING_MARKUP` 作用于所有模型：最终用户费用 = 模型单价 × token 数 × markup。

---

## 四、API 端点参考

所有端点前缀为 `/portal`。

### 公开端点

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /register | 注册 `{email, password, display_name}` |
| POST | /login | 登录 `{email, password}` → `{token, user}` |

### 需要 Bearer Token

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /me | 当前用户信息 |
| POST | /change-password | 修改密码 `{old_password, new_password}` |
| GET | /wallet | 余额 `{balance}` |
| GET | /usage?limit=50&before=RFC3339 | 用量记录（游标分页） |
| GET | /usage/stats?days=30 | 聚合统计（日消费 + 模型分布） |
| GET | /api-keys | 列出所有 Key |
| POST | /api-keys | 创建 Key `{name}` → 含 `key` 明文 |
| DELETE | /api-keys/:id | 吊销 Key |
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
| POST | /admin/users/:id/privileged | 设置/取消用户特权 `{privileged: bool}` |

特权用户额外端点：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /accounts | 列出可绑定的上游账号（id/provider/label/status） |
| PUT | /api-keys/:id/accounts | 改绑某 Key 的上游账号 `{bound_auth_ids: []}`（空=解绑） |
| PUT | /api-keys/:id/unbilled | 切换某 Key 是否免余额 `{unbilled: bool}` |

---

## 五、特权与 Key 的两种独立能力

设计上把两件**互相独立**的事拆开了：

- **`is_privileged`（用户级）** = **能力开关**：只决定"谁被**允许**创建带下面两种特殊属性的 Key"。它本身不影响计费、不影响路由。
- **`bound_auth_ids`（Key 级）** = **账号绑定 / 路由限制**：这个 Key 只能走指定的上游账号。
- **`unbilled`（Key 级）** = **免余额 / 不计费**：这个 Key 跳过余额检查、用量仍记录但不扣钱包。

两个 Key 级属性**任意组合**，互不牵连。所以同一个特权用户可以同时拥有：

| 场景 | bound_auth_ids | unbilled |
|---|---|---|
| 普通付费 | 空 | 否 |
| 付费但只走指定账号（成本隔离） | 有 | 否 |
| 内部免费、只走某账号 | 有 | 是 |
| 内部免费、不限账号 | 空 | 是 |

### 5.1 授予特权（能力开关）

管理员在「管理」页用户列表点「设为特权」（或 `POST /admin/users/:id/privileged {"privileged":true}`）。之后该用户建 Key 时才能勾选下面两项；非特权用户传 `bound_auth_ids`/`unbilled` 会被 403 拒绝。

### 5.2 账号绑定（bound_auth_ids）

建 Key 时在「绑定上游账号」多选框选择（或 API 传 `bound_auth_ids`）：

- **绑 1 个**：固定走它；
- **绑多个**：只在这些账号里，按 CLIProxy 自身的**轮询 / fill-first** 调度；
- **不绑**：走全部账号。

账号 ID 来自 `GET /accounts`——注意 **ID 就是 `auths/` 下的凭证文件名**（如 `gemini-user@x.com.json`）。因此：**改名/删除/换文件名会让绑定失效**（该 Key 请求会因"无可用账号"而失败）。改绑用 `PUT /api-keys/:id/accounts`。

> 绑定限制的是**账号**，不是模型：绑定的账号得能服务你请求的模型（绑 Gemini 账号却请求 GPT 会失败）。

### 5.3 免余额（unbilled）

建 Key 时勾「免余额」（或 API 传 `unbilled:true`，或事后 `PUT /api-keys/:id/unbilled`）。该 Key：跳过 `BILLING_BALANCE_THRESHOLD` 余额检查、用量**仍记录**（含成本与所用账号）但**不扣钱包**；默认仍受限流，除非 `BILLING_UNBILLED_BYPASS_RATE_LIMIT=true`。

> 注意：`unbilled` 是 Key 级属性。取消用户特权**不会**自动改动其已创建的 unbilled Key（要停就吊销那些 Key，或 `PUT /api-keys/:id/unbilled {"unbilled":false}`）。

### 5.4 审计日志

每条用量记录都写入实际服务它的上游账号（`auth_id`/`auth_label`），在「用量明细」的「账号」列可见；unbilled 请求还会打一条 `billing: unbilled request served`（含用户/账号/provider/model）。→ 谁用了哪个账号的额度可追溯。

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
A: 目前需要联系管理员重置。管理员可以通过数据库直接更新密码哈希。
