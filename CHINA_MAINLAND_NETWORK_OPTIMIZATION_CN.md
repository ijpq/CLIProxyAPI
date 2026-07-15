# 中国大陆访问线路与 CLIProxyAPI 稳定性优化建议

结论：这份报告方向基本正确，但混合了“大陆到 Cloudflare”“Cloudflare 到日本 VPS”“代理到上游模型”“Codeg Agent 多轮工具执行”四种延迟。线路和项目都需要优化，不能只靠增加超时解决。

```text
大陆用户 → Cloudflare 接入点 → CF 骨干/Tunnel → 日本 VPS
                                      ↓
                              CLIProxyAPI → 上游模型
                                      ↓
                              Codeg 多轮工具执行
```

## 一、线路方面的判断

普通 Cloudflare 全球网络加 Tunnel，无法保证中国大陆直连稳定。Cloudflare 官方也明确说明：大陆流量跨境访问境外服务器会面临明显的延迟和可靠性问题；其正式解决方案是中国网络，而不是普通橙云或 Tunnel。[Cloudflare China Network](https://developers.cloudflare.com/china-network/)

Tunnel 主要解决源站隐藏和 Cloudflare 到 VPS 的连接，不会自动优化“大陆用户到 Cloudflare 接入点”这一段。Argo Smart Routing 可以优化动态 API 从 Cloudflare 到源站的路径，但不能保证解决大陆用户进入 Cloudflare 的第一段网络。[Argo Smart Routing](https://developers.cloudflare.com/argo-smart-routing/)、[Cloudflare CDN 架构](https://developers.cloudflare.com/reference-architecture/architectures/cdn/)

建议分三档处理：

### 1. 低成本、短期方案

- 保留现有 Tunnel，开启 Argo Smart Routing 做一周 A/B 测试。
- `cloudflared` 使用最新版和 `protocol: auto`，不要盲目强制 QUIC；部分大陆线路 UDP 质量反而较差。
- 日本 VPS 同机或同机房运行两个 `cloudflared` 实例，提高连接器可靠性。Tunnel 本身会维持四条连接到至少两个 Cloudflare 数据中心，但额外 replica 主要提升 HA，并不能显著改善大陆入口线路。[Tunnel 配置与 HA](https://developers.cloudflare.com/tunnel/configuration/)
- SSE 开启 15 秒心跳，减少长时间无数据时连接被中间设备关闭。
- 不建议使用第三方“Cloudflare 优选 IP”作为生产 API 方案，稳定性、安全性和可控性都不足。

### 2. 无 ICP 的实用方案

增加一个香港的“中国大陆优化线路”入口：

```text
大陆用户 → 香港 CN2/CMI/9929 多线入口 → WireGuard/mTLS → 日本 VPS
海外用户 → Cloudflare Tunnel → 日本 VPS
```

使用两个域名，例如：

- `api.example.com`：现有 Cloudflare 全球入口
- `api-cn.example.com`：香港优化线路，DNS-only 或由香港反代直接接入

香港入口只做 TLS、限流和流式反代，数据库、账号和业务仍放日本。需要限制日本源站仅接受香港中转 IP 或使用 WireGuard/mTLS，避免暴露源站。

购买线路时必须分别从中国电信、联通、移动测试晚高峰 P95；“CN2”营销名称本身不能证明质量。

### 3. 正式、预算充足方案

Cloudflare Enterprise + China Network + CDN Global Acceleration。这是官方针对大陆用户和境外动态 API 的方案，但要求：

- Enterprise 计划及单独的 China Network 订阅
- ICP 备案/许可证
- 京东云内容审核

[接入条件](https://developers.cloudflare.com/china-network/get-started/)、[Global Acceleration](https://developers.cloudflare.com/china-network/concepts/global-acceleration/)

需要注意，中国网络的 Load Balancing 不支持 Tunnel/GRE/IPsec 私网 off-ramp，因此当前 Tunnel 架构可能需要调整为受保护的公网源站，具体应先让 Cloudflare 给出架构确认。[China Network Load Balancing 限制](https://developers.cloudflare.com/load-balancing/additional-options/load-balancing-china/)

## 二、报告是否有道理

| 报告结论 | 判断 |
|---|---|
| 正常请求接收速度尚可 | 大致成立，但“首个文字 token”不是严格网络 TTFT |
| 取消请求没有终止上游 | 有现实依据，当前代码确实存在写错误未触发取消的缺口 |
| 401/invalid_grant 隔离太慢 | 成立，尤其 `invalid_grant` 刷新失败分类存在问题 |
| 5xx 需要熔断 | 成立，但不能简单做全局熔断，应按 provider/auth/model 分组 |
| 会话亲和故障时需要解绑 | 原则正确，但项目当前已经具备自动重新选择 |
| 缺少并发公平调度 | 成立，现有只是 QPS token bucket，不是活跃任务数限制 |
| 3–5/10 分钟硬超时 | 不建议直接照搬，会误杀正常的长 Agent 任务 |
| 30 分钟 524 是 Cloudflare | 不准确，普通 Cloudflare 524 默认是 120 秒未收到源站响应 |

Cloudflare 官方说明，524 默认是连接源站成功后 120 秒没有收到响应；报告中的 30 分钟更可能来自上游、WebSocket、流式心跳、其他反代或者客户端自身计时。[Cloudflare 524](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-5xx-errors/error-524/)

另外：

- 4 个超过 5 分钟的任务全部先执行工具，说明 Codeg 的“首 token”很可能是“首段可见文字”，不是模型首事件。
- 184 万累计输入和 80 次模型调用反映的是 Agent 工作量，不等于单个 HTTP 请求接收慢。
- `broken pipe` 是强信号，但只能证明服务端向已断开的连接写数据；要证明上游继续运行多久，还需要共同 request/turn ID。

## 三、当前代码中已经存在和仍缺失的能力

项目已经把下游请求取消桥接到执行 context：[`sdk/api/handlers/handlers.go`](sdk/api/handlers/handlers.go)。HTTP executor 通常也使用该 context 创建上游请求，所以基础取消链路不是完全缺失。

实际缺口是流式写入错误被忽略。例如公共转发器的写回调不返回错误：[`sdk/api/handlers/stream_forwarder.go`](sdk/api/handlers/stream_forwarder.go)，Claude 流直接丢弃 `Write` 错误：[`sdk/api/handlers/claude/code_handlers.go`](sdk/api/handlers/claude/code_handlers.go)。这能解释部分 `broken pipe` 后没有立即取消。

Codex WebSocket 还有一个更具体的问题：非复用连接在 `ReadMessage()` 阻塞时不能立即响应 context 取消，最迟可能等到五分钟读截止时间：[`internal/runtime/executor/codex_websockets_executor.go`](internal/runtime/executor/codex_websockets_executor.go)。

账号方面：

- 401 和 `invalid_grant` 当前通常只隔离 30 分钟：[`sdk/cliproxy/auth/conductor.go`](sdk/cliproxy/auth/conductor.go)。
- 自动刷新停止条件主要识别 401；HTTP 400 的 `invalid_grant` 没有被标记为永久不可重试。这很可能就是 64 轮失败刷新的来源。
- 408/5xx 已经会进入短暂冷却，并非完全没有隔离，但没有连续失败计数、半开探测和 provider 级熔断。
- 会话亲和已经会在绑定账号不可用时重新选择并更新绑定：[`sdk/cliproxy/auth/selector.go`](sdk/cliproxy/auth/selector.go)。

并发方面，当前 billing 限制器只限制请求速率，不限制活跃 Agent 数量：[`internal/billing/guard.go`](internal/billing/guard.go)。

日志方面，当前 request timestamp 是读取完整请求体之后才生成的：[`internal/api/middleware/request_logging.go`](internal/api/middleware/request_logging.go)，因此报告说无法审计“客户端发出到中转收到”的前半段是正确的。

## 四、项目优化优先级

### P0：优先立即实现

1. 所有流式写回调返回 `error`；遇到 broken pipe、connection reset、WebSocket 写失败时立即 `cancel`。
2. Codex WebSocket 在 context 取消时主动关闭或废弃当前上游连接，不能等五分钟读超时。
3. 401 先同步刷新一次；刷新返回 `invalid_grant` 后将整个账号标记为“需重新认证”，不再自动刷新或选择。
4. 增加完整链路字段：
   - `X-Turn-ID`
   - 内部 `request_id`
   - `CF-Ray`
   - 请求进入、body 读取完成、排队、账号选择、上游首事件、下游首写入时间
   - retry 次数、auth ID 哈希、取消来源、写错误
5. 将客户端取消与账号失败分开统计；客户端取消不能降低账号健康分。

### P1：有界排队和基于原因的故障隔离

- 不增加按用户/API Key 的固定活跃任务上限；用户现有的账号访问权限保持不变。
- 不增加每个上游账号 1–2 个高推理请求的固定并发上限。
- 队列必须有长度上限；满时返回 429，而不是无限等待。队列容量和等待语义应作为独立配置，并保持与账号授权无关。
- 不根据“连续两次 5xx”直接熔断。先记录响应来源、状态码、错误体指纹、上游请求 ID、provider、auth ID、模型和失败阶段，区分上游过载、Cloudflare/网关错误、网络中断以及请求特定错误。
- 只有能够归类为同一账号或 provider 的明确可重试故障时，才考虑相应范围的短暂隔离；原因不明确时仅记录和告警，不改变调度状态。
- 不使用或修改现有 `allowed_auth_ids` 来实现优先级或独立账号池；该字段继续只表达 portal 中已有的用户账号访问权限。

### P2：控制 Agent 会话成本

- 把“代理重试次数”和“Codeg 模型调用次数”分开记录。
- 长会话阶段性总结到项目文件，然后新建活跃会话。
- 对单轮调用数设置软告警，例如 12 次提醒、30 次要求确认；不建议直接按 token 硬截断。

## 五、建议先采用的配置基线

```yaml
request-retry: 1
max-retry-credentials: 2
max-retry-interval: 5
disable-cooling: false
save-cooldown-status: true
transient-error-cooldown-seconds: 180

routing:
  strategy: round-robin
  session-affinity: true
  session-affinity-ttl: 30m

streaming:
  keepalive-seconds: 15
  bootstrap-retries: 1
```

暂时不要启用 `nonstream-keepalive-interval`：当前实现会提前写空白字符并提交 HTTP 200，之后上游失败时可能无法再返回正确错误状态。

## 六、如何证明到底是哪段线路慢

从电信、联通、移动分别在早晚高峰连续测试：

```bash
curl -sS -o /dev/null \
  -w 'ip=%{remote_ip} dns=%{time_namelookup} tcp=%{time_connect} tls=%{time_appconnect} ttfb=%{time_starttransfer} total=%{time_total}\n' \
  https://你的域名/v1/models
```

Cloudflare 侧重点记录：

- `ClientTCPRTTMs`
- `EdgeColoCode`
- `EdgeTimeToFirstByteMs`
- `OriginTCPHandshakeDurationMs`
- `OriginResponseDurationMs`
- `RayID`

这些字段可以把“大陆到 CF”和“CF 到源站”拆开。[Cloudflare HTTP 日志字段](https://developers.cloudflare.com/logs/logpush/logpush-job/datasets/zone/http_requests/)、[Origin Analytics](https://developers.cloudflare.com/speed/origin-analytics/)

最合理的执行顺序是：先做 P0 取消传播、失效账号隔离和链路日志；同时对 Argo 与香港优化入口进行一周三网 A/B。没有这些分层指标，单纯更换 VPS、Cloudflare IP 或增加超时都只能碰运气。
