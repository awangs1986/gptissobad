# 绝对中文屏蔽与译文持久化 Spec

> 状态：拦截姿态部分作废。§2 的 fail-closed 拦截面、§7 验收中的 403 条目已被
> `translation-policy.md`（最高级别）改为 fail-open：内容侧中文放行，只有
> 额度/翻译器故障/连不上才 403。缓存持久化（§3）、会话键（§4）、自启与上游
> 超时（§5）、观测（§6）各节继续有效，全文保留作历史。

本文件记录 2026-09-14 一轮设计质询的结论。每条都写了被否决的方案和
否决理由，避免以后重新讨论一遍。

## 1. 目标

**硬约束：上游收到中文会导致账号被封。** 因此本项目的目标不是「让 Codex
能读中文」，而是**上游绝不收到中文**。让 Codex 可用是手段，不是目的；两者
冲突时，宁可这一轮 403，也不能放行。

唯一例外是**文件路径与 URL 里已有的汉字**。Codex 自己会把工作目录注入
请求，改不掉，只能豁免。

次要目标：重启电脑或长时间搁置后，不因缓存丢失而重复付费翻译同一段话。

## 2. Han-free Invariant 的新范围

| | 旧 | 新 |
| --- | --- | --- |
| 检查范围 | 请求体里的 user-text 散文 | **出站请求的全部五个面**（见 2.1） |
| 例外 | 路径 | 路径（不变，且只限路径本身） |
| 解不开的请求体 | 放行 | **403** |
| 命中后 | 403 | 403 |

旧范围实际只覆盖「POST 且能解析成 JSON 的请求体里的 user 散文」。一轮
代码级排查发现另外四个面今天完全没有检查，请求原样出站。下面五个面必须
全部覆盖，缺一个这条不变式就不成立。

命中后一律 403：user / assistant 散文走翻译通路（见 2.2），其余各面只有「放行」和「403」
两种结果。

### 2.1 出站面清单

**(a) JSON 请求体的任何位置。** 实现上是**删掉** `walkItems` 现有的三处
特例，不是新增检查：

1. `role != "user"` 的条目直接 `continue`，完全不查；
2. `role == ""` 的条目只查 `content` 键，而 `function_call_output`
   用的是 **`output`** 键，查不到；
3. `instructions` 在顶层，`walkUserTexts` 只遍历 `input` / `messages`，
   根本不看它。

非 user 位置同样使用 `hasHanOutsidePaths`，不是裸的汉字判断。否则
`pwd`、`ls` 的输出里带着中文工作目录，每次工具调用都会 403。

**(b) 非 POST 的请求体。** `internal/gateway/gateway.go` 175-178 行：

```go
if r.Method != http.MethodPost {
	g.logf("proxy %s %s", r.Method, path)
	g.proxy(w, r, r.Body)
	return
}
```

PUT / PATCH / DELETE 的请求体连读都不读，直接流给上游。今天 Codex 在这条
路上只发 `GET /v1/models`（没有请求体），实际风险低，但这个分支在构造上
就是 fail-open。规则：**方法不参与判断**，只要带请求体就读出来按 (a)(c)
检查。

**(c) 解不成 JSON 的请求体。** 同文件 187-196 行：`needsRewrite(body)` 在
`json.Unmarshal` 失败时返回 false，代码随即回退到 `hasHanBytes(body)`——
它扫的是**原始 wire 字节**。gzip 过的中文里没有字面汉字字节，扫描通过，
请求体连同 `Content-Encoding` 头一起被转发。全仓库对 `Content-Encoding`
和 `gzip` 的引用数是 **0**：既没有解压，也没有拒绝。`multipart/form-data`
里夹一段中文同理。

规则：**网关解不出能扫描的 JSON，就 403，不转发。** 解不开的请求体按
不可信处理，不按无害处理。不为此新增解压通路——目标是拦住，不是多支持
几种编码。

**(d) 查询串。** 同文件 679-680 行：

```go
target := *r.URL
dest := g.upstream.ResolveReference(&url.URL{Path: target.Path, RawQuery: target.RawQuery})
```

`RawQuery` 原样拷过去，没有任何检查。这里和路径是**不对称**的：路径确实
需要携带汉字（那就是唯一的豁免），查询串没有这个需求。所以路径继续豁免，
查询串严格判断。

但**不能直接扫 `RawQuery`**：查询串是百分号编码的，`?note=中文` 到了
`RawQuery` 里长成 `note=%E4%B8%AD%E6%96%87`，裸汉字扫描一个字都扫不到，
规则会形同虚设。必须先 `url.ParseQuery` 解码，扫解码后的键和值；原始
`RawQuery` 也要扫一遍，因为 Go 允许未编码的 UTF-8 原样留在里面。

**(e) 请求头。** 同文件 686 行的 `copyHeaders(out.Header, r.Header)` 把每
一个非逐跳头原样拷给上游，不扫汉字，带中文标题或标签的头会直接出站。头
同样按严格判断处理；若日后真有某个头携带中文工作目录，403 会先把它暴露
出来，再单独决定要不要豁免。

头只扫原始字节。RFC 2047 编码词（`=?UTF-8?B?...?=`）那类形式扫不到，但
没有任何正当理由把散文编码进请求头，真出现了再单独决策。

### 2.2 进翻译通路的位置

只有两个 role 的 `content` 进翻译通路，其余全部 403：

- **`role=user`**：提示词本身。
- **`role=assistant`**：模型自己的历史回复。回复指令让它用中文回答，Codex
  每一轮都把上一轮回复当历史回传——留在 403 上，任何多轮到第二轮必死
  （issue 12 实录）。走和 user 完全相同的通路：遮路径、翻译、同一份缓存、
  复检；`output_text` 分片与 `input_text` 同等对待。成本有界：一条回复只在
  首次进入历史的那一轮翻一次，之后命中缓存。连贯性不受影响：模型看到的是
  一份全英文、内部一致的记录，加一条常驻的中文回复指令，回复语言由指令
  决定，不由历史决定。

### 2.3 仍然 403、且以后也不翻译的位置

- **工具输出与 `function_call.arguments`**。这是磁盘上的真实内容。翻成英文
  后模型看到的和文件对不上，接下来搜索替换找的是不存在的字串——这是正确性
  问题，与 token 无关。日后处理用**带标记的遮盖**（汉字段替换为非中文标记，
  模型知道有内容被拿掉），不用翻译。现阶段 403。
- **`instructions` / developer / system 消息**。中文 `AGENTS.md`、自定义
  指令。403 会点名这一面，用户把那几个文件改成英文是一次性的事。

因此当前的真实边界是：**多轮能活，直到模型往文件里写了中文**（那次调用的
`arguments` 带中文，下一轮历史回传即 403）。这不是 2.2 没修好，是上一条
还没做。

## 3. Translate Cache 持久化

### 3.1 存什么

`~/.codex/translate-cache.json`，扁平的 `{sha256: 英文译文}`。

键是 `sha256(strategyVersion + model + translatorSystem + chunk)`。
**`translatorSystem` 必须进键**，否则改了翻译提示词想让译文变好，旧条目
照样命中，改动等于没生效——这类 bug 极难排查。

值里带着 `__CLP_n__` 路径占位符：chunk 是在 `maskHanPaths` **之后**才切分
和取哈希的。由此得到两个性质，都是想要的：

- 缓存文件里**一个汉字都没有**，连路径都没有；
- 同一句话在不同中文目录下命中同一条缓存，取回后 `unmaskHanPaths` 填回
  当前路径。切换项目目录不会让缓存失效。

### 3.2 不做容量管理

1M 上下文封顶，重度使用一天约 50 条提问、源文加译文约 30 KB，**一年也就
十几 MB**。不加上限、不加时间戳、不加 LRU、不加 schema 版本号。

格式以后若要变，`loadCache` 解析失败已经会「当空的重来」，代价是重译
一次，兜底早就存在。

### 3.3 落盘时机

脏标记 + **2 秒 debounce**，并在 `Runtime.Stop()` 里 flush（Watchdog 的
信号处理已经会调它，覆盖重启、注销、Ctrl-C）。

覆盖不到的是断电和 `kill -9`，最多丢最近 2 秒新译的条目，下次重译一遍。
**只花钱，不影响正确性**——Han-free 和 fail-closed 都不依赖这个缓存。

否决「每条都写」（当前行为）：全量重写整个文件，长对话首轮就是 O(n²)。
否决「只在退出时写」：`kill -9` 一次丢掉整轮对话的全部译文。

### 3.4 谁能写

**只有 Watchdog 里的 Local Gateway 落盘。** `codex-translate` 只读不写。

今天的第二个写者是 `cmd/codex-translate/main.go:35`：它把 `CacheFile` 指
向 `~/.codex/translate-cache.json`，和 `internal/control/control.go:534`
给 Watchdog 内嵌网关配的是同一个文件。两个进程各自持有完整 map 的内存
副本，`cacheSet` 每次都写一份全量快照，后写的会把先写的条目整个抹掉——
不是交错，是丢数据。只是重复付费和延迟，不是汉字泄漏。`codex-translate`
是无 UI 备用路径，平时不跑，它新译的条目丢失可以接受。README 要写明
这一点。

否决「写到另一个文件」：分裂缓存，命中率对半砍，还多一个要清理的文件。

### 3.5 隐私定位

缓存文件里存的是译文，**等价于一份英文版的提问历史**，明文、永久、可 grep。
这是**明知故为**：单机个人使用，物理安全就够，加密是自欺（密钥也在同一台
机器上）。文件权限 `0600`。

此决定另记 ADR，避免以后的架构审核把它当成隐私泄漏「顺手修好」，导致每次
重启后又开始重复付费。

### 3.6 不做清空按钮

缓存里的译文**不可能含中文**（`translateOnce` 在写缓存前就会拒掉含汉字的
译文），所以清空不是安全功能，只是「翻得不好想重来」的质量功能。
`rm ~/.codex/translate-cache.json` 即可，不值得为它动 API、前端和状态。

## 4. 会话键

从 `requestSessionKey` 的候选列表里**删掉 `previous_response_id` /
`previousResponseId`**。

这个 ID 每轮都变（它就是上一次 response 的 ID），当会话键是自相矛盾的：
每轮都算新会话 → `isPrefix` 永不匹配 → 永远全量重建，增量翻译完全失效；
同时每轮往 `sessions` 塞一条新记录，一路涨到 `maxSessions` 才开始淘汰。

删掉后这类请求走「无会话键」路径，本来就是全量，但不再堆垃圾记录。

否决「用它链式追溯上一轮会话键」：要维护 response_id → session 映射表，
为一个还没验证过的模式增加状态。

## 5. 进程生命周期

重启电脑后，SessionStart 才去拉 Watchdog 和前置，开会话后的第一轮仍可能
403。**改为开机自启**：`install-plugin.sh` 往 `~/.config/autostart/` 装一份
desktop 文件（XFCE 与 Cinnamon 同路径），复用 `pack/appimage/codex-lang.desktop`。

SessionStart 插件保留，作为「进程崩了」的补救；403 继续兜底。

否决 systemd user unit：托盘图标要求图形会话，得额外处理
`After=graphical-session.target` 之类的排序，为本机个人工具增加排障面。

否决「后端没起来时排队而不是 403」：排队会让 Codex 干等，那正是当初选
403 而不是连接被拒要避免的体验。

### 5.1 上游 client 必须有超时

`internal/gateway/gateway.go` 114 行是 `client: &http.Client{}`——翻译用的
`translate` client 带 `Timeout`，转发给上游的这个没有。上游挂住时，一个
goroutine 加一条连接一直占着，直到客户端自己断开。这不是汉字泄漏，但它
从另一头绕开了上面那条否决：调用方等到的是干挂，不是 403。给它设超时，
超时即按 fail-closed 返回 403。

## 6. 观测

**不加新埋点。** Control Page 上已有的 `cacheHits` 和 `translationRequests`
就是命中率：`cacheHits / (cacheHits + translationRequests)`。用一段时间
看一眼，低了再谈 chunk 归一化。

否决「按 probe playbook 抓真实请求体做多轮对比」：那些文件里全是中文提问，
仓库政策本来就禁止落盘。

注：仓库里**没有任何真实 Codex 请求体的抓包**，「重启后旧消息逐字节相同、
能命中缓存」目前只是设计假设，未经验证。上面的命中率就是验证手段。

## 7. 验收标准

- `instructions`、developer / system 消息、`function_call_output`、
  `function_call.arguments` 里的中文散文一律 403，不转发；
- assistant 历史里的中文散文被翻译后转发；同一段历史再次回传时不再调用
  Translator Backend；
- 上述位置里的中文**路径**不触发 403；
- 非 POST（PUT / PATCH / DELETE）但带请求体的请求，体内有中文散文时 403；
- gzip 压缩或 `multipart/form-data` 等解不成 JSON 的请求体一律 403，不看
  wire 字节里有没有汉字；
- 查询串里的汉字无论原样还是百分号编码（`%E4%B8%AD`）都 403，请求头里出现
  汉字时 403，同一请求路径里的汉字仍不触发 403；
- 上游不响应时在超时后返回 403，不无限挂起；
- 缓存文件里不出现任何汉字，且包含英文译文；
- 进程重启后，相同原文命中缓存，不再调用 Translator Backend；
- 改动 `translatorSystem` 后旧缓存不再命中；
- 只发 `previous_response_id`、不发 `conversation_id` 的连续请求不会在
  `sessions` 里堆积记录；
- 开机后不开 Codex 会话，`curl 127.0.0.1:18787/healthz` 即可返回 200。

### 7.1 必须反转的现有测试

这两个测试今天断言的行为与硬约束直接冲突，§2 落地时必须改成断言 403，
不能只是删掉：

- `TestNonUserHanIsLeftAlone`（`internal/gateway/gateway_test.go`
  326-352 行）：断言 `instructions`、developer 消息、
  `function_call_output.output` 里的中文原样到达上游；
- `TestChatCompletionsUserMessageIsRewritten`（同文件 687-707 行）：断言
  `role=system` 的 `"用中文。"` 活着送到上游。这条用例里 user 散文被翻译
  的断言保留，只反转 system 那一条。

### 7.2 目前完全没有覆盖、必须补的场景

- 多轮对话：历史里上一条 assistant 回复是中文（`TestAssistantHistoryIsTranslated`
  已覆盖翻译与缓存命中；`TestSystemAndToolHistoryStayFailClosed` 钉住
  developer / 工具输出仍 403）；
- gzip 压缩的 POST 请求体；
- 非 POST 且请求体带中文；
- 查询串带中文（原样与百分号编码两种形式）；
- 请求头带中文。
