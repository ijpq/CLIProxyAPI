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

---

## 五、常见问题

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
