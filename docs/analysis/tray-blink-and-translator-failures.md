# 两个缺陷的代码级诊断：托盘闪烁 / 翻译服务"经常访问不了"

只做代码走查，未运行任何东西（没有 Go 工具链，也没有真机）。所有结论都给出
`文件:行`，能证实的写"确定"，只能推断的写"可能/需实测确认"。

摘要：

- **闪烁图标**：链路里有 6 个断点，其中 3 个是"必然不闪"的硬故障（`ok` 门、
  翻译根本没发生、只发 PropertiesChanged 不发 SNI 专用信号）。用户看到的
  "图标在、永远不变"最可能落在"协议信号"+"`ok` 门"这两条上。
- **翻译服务访问不了**：用户"包过大"的直觉**方向对了一半**——代码里
  **完全没有分片大小上限**；但真实失败形态不是 413，而是
  **15 秒整条请求超时**、**静默截断**、**限流被误报成"额度用尽"**，而且
  **所有失败在日志里都被压成同一行**，所以看起来像"随机不可达"。

---

# 一、托盘闪烁为什么完全无效

## 1.1 链路（每一环都能单独掐死闪烁）

```
Gateway.doTranslate()          gateway.go:784-790   翻译 HTTP 在飞行中：translating++
        ↓
Metrics().Translating          gateway.go:166-171   Load() > 0
        ↓
stateLocked()                  control.go:245-283   Translating: metrics.Translating (278)
        ↓
watchdog status 闭包            cmd/codex-watchdog/main.go:70-82
        ↓
tray.Run 每 500ms tick()        tray_linux.go:53,117-142
        ↓
blinkDim()                     tray_linux.go:147-157  需要 ok==true
        ↓
emit()                         tray_linux.go:104-114  只发 PropertiesChanged
```

## 1.2 断点 1：`ok` 门——`HasKey` 与小米路线不匹配（必然不闪）

`main.go:74`：

```go
ok := st.Enabled && st.Running && st.HasKey && (!st.FrontEnabled || st.FrontReachable)
```

`blinkDim` 在 `!ok` 时直接返回 false（`tray_linux.go:154`），所以只要 `ok=false`，
图标恒红、**永不闪**。

其中 `st.HasKey` 的来源是 `opencodego.ResolveKey`（`control.go:155-157` →
`opencodego/auth.go:33-44`），只认三个来源：

1. `~/.codex/opencode-go.key`
2. `~/.pi/agent/auth.json` 里的 `opencode-go`
3. `$OPENCODE_API_KEY`

而 README 推荐、作者自己用的**小米 token 专线**，key 放 `~/.codex/mimo.key` 或
`$MIMO_API_KEY`（`internal/gateway/translator_route.go:14-17,22-32`）——**不在这三个
来源里**。

后果：mimo-only 配置下 `HasKey=false` → `ok=false` → 红图标、不闪、不报错；同时
Control Page 的提示也是错的（`internal/control/frontend/app.js` 的 keyHint 只提
opencode-go / `OPENCODE_API_KEY`，从不提 `~/.codex/mimo.key`），用户会以为"密钥
没配"。

## 1.3 断点 2：翻译根本没发生（mimo-only 时，闪烁无从谈起）

`gateway.go:492-499`：

```go
func (g *Gateway) translateTextWithCache(...) {
	if !hasHanOutsidePaths(text) { ... }
	masked, heldPaths := maskHanPaths(text)
	if strings.TrimSpace(g.cfg.APIKey) == "" {        // 497：OpenCode key
		return "", false, errForwardOriginal
	}
```

`cfg.APIKey` 是 **OpenCode** 的 key（`control.go:560-566` 只传 `APIKey: key`，从不设
`MimoAPIKey/MimoBaseURL`）。也就是说：**即使 `~/.codex/mimo.key` 存在、mimo 路由
可用，只要 OpenCode key 为空，翻译一步都不会走**——直接
`errForwardOriginal` → `ServeHTTP:250-253` 原样转发、不贴 Reply Instruction、不计数、
`translating` 恒 0 → 不闪。日志特征：`proxy POST /v1/responses untranslated`。

另外这条路径让"发送测试消息"必然失败：`gateway.go:176-196` 的 `CheckTranslator`
同样先卡 `cfg.APIKey`，mimo 用户点测试按钮只会看到
`未找到 OpenCode Go 登录凭据`（`control.go:435-444`）——**这也是"访问不了翻译服务"
的一个直接来源（见问题二 H）**。

## 1.4 断点 3：跨进程时计数器恒为 0

`Translating` 只反映**托盘所在进程**的 `r.gateway`（`control.go:249-251`）。如果真正
在翻译的是另一个进程：

- `codex-translate`（无 UI 版网关，`cmd/codex-translate/main.go`）；
- 或另一个 watchdog 实例把真网关挪到了别的端口（`control.go:519-575` 有端口回退链），

那么托盘进程里的计数器永远是 0 → 不闪，而翻译在外人看不到的地方正常进行。

判定：`curl -s http://127.0.0.1:18786/api/state` 里 `running/translating/metrics`
是否随翻译变化；以及 Codex 的 TOML 指向的端口是谁在听。

## 1.5 断点 4：只发 PropertiesChanged，从不发 SNI 专用信号（最可能）

`tray_linux.go:104-114` 是整个文件里**唯一**的出站通知：

```go
emit := func(icon []pixEntry, tip tip) {
	iconProp.Value = icon
	tipProp.Value = tip
	_ = conn.Emit("/StatusNotifierItem", "org.freedesktop.DBus.Properties.PropertiesChanged",
		"org.kde.StatusNotifierItem",
		map[string]dbus.Variant{ "IconPixmap": ..., "ToolTip": ... }, []string{})
}
```

问题点：

1. **从不发 `NewIcon` / `NewToolTip` / `NewStatus`**。SNI 规范要求属性变化时同时发
   这些专用信号（参考 KDE 的 scenario 文档与各 host 实现）；不同面板实现
   （xfce4-panel 内建 systray / XApp applet / ayatana-indicator-application 代理 /
   gnome appindicator 扩展）对 `PropertiesChanged` 的处理并不一致，只认专用信号的
   实现会把首次 `GetAll` 拿到的 pixmap 一直当缓存用 → **图标在，但永远不变**。
   这与"绿/红也许能变、但闪烁完全无效"的现象一致（若 host 连 PropertiesChanged
   都不理，则连红/绿也不变）。
2. **`IconName` 恒为空串**（`tray_linux.go:71-83`），所以没有"切换主题图标名"
   这条本来最可靠的兜底路径。
3. **`Status` 恒 `"Active"`**，且没有导出 `AttentionIconPixmap` / `AttentionIconName`
   —— 面板唯一被设计用来"动起来"的机制（NeedsAttention + attention icon）
   完全没用上。
4. `_ = conn.Emit(...)` **吞掉所有错误**：如果 variant 构造或序列化失败，闪烁会静默
   消失，日志里没有任何痕迹。

"不确定仍需实测"的部分：具体是哪个 host 忽略 PropertiesChanged。代码层面能确定的
是：**只发了这一种信号，而且没有任何后备通道**。

## 1.6 断点 5：注册失败后永久放弃（图标会整场会话消失）

`tray_linux.go:96-102`：

```go
call := conn.Object("org.kde.StatusNotifierWatcher", "/StatusNotifierWatcher").Call(
	"org.kde.StatusNotifierWatcher.RegisterStatusNotifierItem", 0, name)
if call.Err != nil { conn.Close(); return call.Err }
```

`cmd/codex-watchdog/main.go:82-86`：`tray.Run` 返回错误 → 打一行
`panel icon unavailable` → `select{}` **永久放弃**（进程继续服务 Control Page）。

而安装脚本把 watchdog 写进了**开机自启**（`scripts/install-plugin.sh:37-43`，
`Exec=$BIN_DIR/codex-watchdog`），登录时面板/SNI watcher 往往还没起来 →
注册失败 → **这一整个登录会话都不会再有图标，也不会闪**（面板后来起来了也不重试；
面板重启 `xfce4-panel -r` 之后同样不会重新注册，因为没有订阅
`org.kde.StatusNotifierWatcher` 的 `NameOwnerChanged`）。同类已知问题（docker/for-linux
仓库里那条 GNOME/SNI 报告）给出的建议正是"watcher 晚出现时要重试注册"。

## 1.7 断点 6：轮询采样 + 数据竞争（会让闪烁"看起来完全无效"）

- 闪烁靠**每 500ms 轮询一个"在飞行中"的计数器**（`tray_linux.go:53`），不是事件
  驱动。单次 JSON 翻译通常 1–3 秒，配合 `blinkHold = 5s`（`tray_linux.go:147-157`）
  一般能看见；但命中缓存、纯英文轮次、`translateTextWithCache` 提前返回的轮次
  **一次网络调用都没有**（`translating` 不增），这些轮次本来就不该闪。
- `emit` 直接写 `iconProp.Value` / `tipProp.Value`（`tray_linux.go:105-106`），而
  godbus 的 Properties 处理线程会**无锁**读同一个 `prop.Prop.Value`——这是真实的
  data race（`go test -race` 能抓到）；撕裂的值可能让 host 拿到坏 variant 并丢弃
  这次更新。

## 1.8 一句话结论

| 现象 | 最可能的断点 |
| --- | --- |
| 有图标，永远不变/不闪 | 1.5（只发 PropertiesChanged） |
| 图标常红，翻译时也不闪 | 1.2 / 1.3（`ok` 门、mimo key 不认、翻译没发生） |
| 有时干脆没有图标 | 1.6（注册失败不重试，且自启动早于面板） |
| 闪烁有一搭没一搭 | 1.7（500ms 轮询、竞争） |

---

# 二、翻译的时候"经常访问不了翻译服务"

## 2.1 实际调用链与真正的请求体

```
Codex → 前置(fronthost) → 网关 ServeHTTP (gateway.go:212-270)
   → rewrite (280-311) → walkTranslatableTexts (guard.go:26-98，只走 user/assistant 正文)
   → translateTextWithCache (492-570)
        ├─ maskHanPaths：汉字路径摘成 __CLP_n__ 占位符
        ├─ 按 "\n" 切块，密钥行（sk- / Bearer / api_key=）整行保留不译
        └─ 每个"连续非密钥行段" = 1 个 chunk = 1 次 HTTP 请求
   → translateChunkCached (588-640) → translateOnce (642-678) → callTranslator (792-826)
   → 失败则 translateFallbackOnce (680-736，Responses 方言)
```

## 2.2 原因 A：没有任何分片大小上限（"+包过大"的直觉成立）

`gateway.go:500-540` 的切块只有两个维度：**换行** 和 **是不是密钥行**。没有字节数、
没有 token 数、没有"超长就再切"的逻辑，`Config` 里也没有相关旋钮
（`gateway.go:47-73`）。

后果：一条 user 消息（或一条 assistant 历史、或粘贴的一大段文档、甚至某个超长的
单行）就是**一次请求的 `messages[1].content`**。10 万字中文 ≈ 300KB body，一次发出去。

旁证：作者自己在 spec 里假设的是小包——"重度使用一天约 50 条提问、源文加译文约
30 KB"（`docs/spec/han-blocking-and-cache.md` §3.2），所以从没做上限；
`docs/spec/context-translation-incremental.md` §6 写的"每会话 1000 条 / 32 MiB"
限制**代码里没有实现**（只有 `maxSessions = 1000` 和 30 分钟 TTL，
`gateway.go:27-29`）。

## 2.3 原因 B：15 秒是"整条请求"的预算，大包必然超时

- `defaultTimeout = 15 * time.Second`（`gateway.go:26`），`TranslateTimeout` 为 0 时
  生效（`gateway.go:133-135`）；**Control Page 从不设置它**（`control.go:560-566`），
  `codex-translate` 也不设 → 全仓库实际就是硬编码 15s。
- `g.translate = &http.Client{Timeout: timeout}`（`gateway.go:157`）。Go 的
  `Client.Timeout` **覆盖 DNS + 拨号 + TLS + 上传 + 排队 + 生成 + 下载整条链路**。
- `callTranslator` 又叠一层同样的 15s context（`gateway.go:793`）。

翻译场景下输出长度 ≈ 输入长度：2000 汉字 ≈ 1500+ 输出 token，小米/OpenCode 这条
链上的廉价模型 15 秒内吃不下 → **超时 → `errors.New(failClosedMsg)` →
用户收到 403 `翻译失败，这一轮未发送`**。会话新建时是全量重建（`rewrite` 里
`walkTranslatableTexts` 串行遍历所有正文），历史越长越容易踩到。

## 2.4 原因 C：包大时**不会报错，而是静默截断**（比 403 更坏）

- 请求体里**没有 `max_tokens` / `max_output_tokens`**（`callTranslator:795-801`；
  research 文档明确列了这个可归一化字段）。
- `parseTranslation`（`gateway.go:827-856`）只看 `choices[0].message.content` 是否
  非空，**完全不看 `finish_reason`**；Responses 方言那条
  （`parseResponsesTranslation:738-783`）同样不看 `incomplete`。

后果：超长 chunk 被服务端按默认输出上限截断 → 半句英文照样通过
`hasHanOutsidePaths` 检查（`gateway.go:668`）→ 照样写进缓存 → 照样当用户提问发给
上游。用户的"提问"变成半截，模型答非所问，而网关日志显示 `translated`。

## 2.5 原因 D：所有失败在日志里被压成同一行（"随机不可达"就是这么来的）

`ServeHTTP:254-257`：

```go
g.metric(func(m *Metrics) { m.Rejected++ })
g.logf("fail-closed %s %s", r.Method, path)
http.Error(w, err.Error(), failClosedStatus)
```

日志**不记 status code、不记 endpoint、不记请求字节数、不记 chunk 大小、不记
`err`**（只有 403 body 里那句中文，且超时/4xx/5xx/解析失败在多数路径上被换成同一个
`failClosedMsg`，见 `translateOnce:643-671`）。所以"经常访问不了翻译服务"这句话，
用现有日志**无法区分**是超时、400、401、429 还是代理问题。

## 2.6 原因 E：429 被一律当成"额度用尽"，且不重试、不退避

`translateOnce:650-651`：

```go
if status == http.StatusTooManyRequests || status == http.StatusPaymentRequired {
	return "", false, errors.New(quotaMsg)     // retry = false
}
```

而 `docs/spec/translation-policy.md` §5 第 1 条规定"额度用完"是 **429/402 且正文
点名"额度"**——代码根本没有看响应体。于是任何**限流**（廉价模型常见）都被报成
"翻译额度用尽，这一轮未发送"，既不重试也不退避，用户读到的是"服务不给用了"。

配合 OpenCode Go 的 5 小时额度/请求数上限（research 文档里的模型表），一次全量重建
几十个请求就可能撞限流 → **"经常"**。

## 2.7 原因 F：重试 = 立刻重发同一个大包，0 退避

`translateChunkCached:597-612`：

```go
for attempt := 0; attempt < 2; attempt++ {
	out, retry, err := g.translateOnce(ctx, chunk)
	...
	if !retry { break }
}
```

- 最多 2 次主尝试（transport 错误 / 5xx 才 retry），**没有任何 sleep/backoff/jitter**；
- 之后还有 1 次备用模型（Responses 方言，`gateway.go:619`，同样 15s）；
- 最坏 ≈ 15 + 15 + 15 = **45 秒**才回 403，期间前置 `fronthost` 的转发
  （`internal/fronthost/fronthost.go:161-200`）是无超时的流式拷贝，Codex 侧干等，
  体感就是"翻译服务挂了"。

## 2.8 原因 G：只有翻译 client 走系统代理，上游不走（"访问方式"的实证点）

- 翻译：`&http.Client{Timeout: timeout}`，**没设 Transport** → 用
  `http.DefaultTransport` → 认 `HTTPS_PROXY/HTTP_PROXY/NO_PROXY`、启用 HTTP/2
  （`gateway.go:157`）。
- 上游：手写 `&http.Transport{DialContext, ResponseHeaderTimeout}`，**`Proxy` 字段为
  nil** → 不走任何代理（`gateway.go:145-152`）。

一套代码两种网络语义。CN 环境下 `HTTPS_PROXY` 常开着（代理规则/节点抖动很常见），
于是只有**翻译请求**被塞进代理，一抖就"访问不了翻译服务"，而本地下一跳
（Cockpit/localhost 之类）完全不受影响。这也是"经常、随机"的典型来源。

## 2.9 原因 H：密钥路线不一致（与问题一 1.2/1.3 同源）

`HasKey` / `CheckTranslator` / UI 提示只认 OpenCode 那条链
（`opencodego/auth.go:33-44`、`control.go:155-157,435-444`、
`internal/control/frontend/app.js` 的 keyHint），而真正干活的 key 可能是
`~/.codex/mimo.key`。用小米专线时：

- Control Page "发送测试消息" → `未找到 OpenCode Go 登录凭据`；
- 网关侧若 OpenCode key 为空，则连翻译都不发生（`gateway.go:497`）。

两种情况都会被用户读成"访问不了翻译服务"。

## 2.10 与"包过大"假设的对照

| 你的猜测 | 代码事实 |
| --- | --- |
| 发送包过大 | **成立**：无任何大小上限（2.2），整段正文=一次请求 |
| 会被服务端拒收 | 未必是 413：更常见的是 **15s 超时**（2.3）或 **静默截断**（2.4） |
| "访问方式"有问题 | **另一个独立问题**：翻译走环境代理、上游不走（2.8） |
| 感觉随机不可达 | **部分是观测问题**：失败原因全被压成一行日志（2.5），429 被误报成额度（2.6） |

---

# 三、建议修法（按性价比排序）

问题一（托盘）：

1. `emit()` 里同时发 SNI 专用信号：`NewIcon`、`NewToolTip`（无参数）和
   `NewStatus("Active"/"NeedsAttention")`；翻译中把 `Status` 切到 `NeedsAttention`
   —— 这是面板本来就支持"动起来"的机制，比抢 pixmap 刷新可靠得多。
2. `ok` 与 `HasKey` 解耦：闪烁只看"翻译器在飞行中"，不要被红色门挡掉；
   `HasKey` 改成"**当前生效路线**的 key 存在"（mimo → `ResolveMimoKey()`）。
3. 注册加上重试：起不来时按 0.5/1/2/5s 退避重试，并订阅
   `org.kde.StatusNotifierWatcher` 的 `NameOwnerChanged`，watcher 回来了就重注册。
4. 把轮询换成事件/更快的采样（如 150–200ms，或由 Control Page 通过 SSE 推送
   `translating`），并保留 `blinkHold`。
5. 修掉 `prop.Value` 的竞争（加锁或走 `prop` 包的 Set/Emit 通路），并且**别吞**
   `conn.Emit` 的错误——首次失败打一行日志。

问题二（翻译）：

1. **按大小切片**：给 chunk 加字节/字符上限（如 1–2KB 或按 token 估），超长内容
   切成多次调用再拼回；这样 15s 的预算才够用。
2. **超时可配置且按包长缩放**：`TranslateTimeout` 提到 Control Page；大 chunk 用
   `ttfb + f(size)` 而不是死 15s。
3. **设 `max_tokens` 并检查截断**：读 `finish_reason` / Responses 的 `incomplete`，
   截断按失败处理（不要静默把半句话发给上游）。
4. **429 语义修正**：只有响应体点名"额度"才走 `quotaMsg`；普通 429 可重试，
   加退避（如 1s/3s），与 `translation-policy.md` §5 对齐。
5. **日志记因**：`fail-closed` 那行补上 status、目标 host、请求字节数、错误类别
   （绝不记正文/密钥）；这一条能立刻把"经常访问不了"变成可定位的问题。
6. **代理策略显式化**：给翻译 client 一个明确的 `Transport`（`Proxy` 取
   `ProxyFromEnvironment` 或显式关闭，加 `DialContext/TLSHandshake/ResponseHeader` 分段
   超时），并在文档里写清 `NO_PROXY` 建议；同时决定上游是否也要走代理（一致性）。
7. **密钥来源统一**：先解析"生效目标"，再取对应 key；`CheckTranslator`、`HasKey`、
   UI 提示都要覆盖 mimo 路线（`~/.codex/mimo.key` / `$MIMO_API_KEY`）。

---

# 四、无法从代码判定的部分（需要真机才知道）

- 具体面板 host 是否忽略 `PropertiesChanged`（只能确定代码没发别的信号）。
- 你的网络里 `HTTPS_PROXY` 是否开着、翻译域名是否真的被代理劣化。
- 你踩到的到底是超时、400、429 还是限流——**这取决于日志**，而现在的日志不记这些
  （见 2.5），所以第 5 条修法应该最先做。
