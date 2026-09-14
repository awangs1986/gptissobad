# 上下文增量翻译 Spec

## 1. 目标

Local Gateway 为每个会话维护一份“原始上下文”和一份“已翻译上下文”。首次收到请求时建立快照；后续请求只翻译新增或发生变化的 user 内容，再按原始消息顺序组装完整请求。

该策略需要满足：

- 相同输入在同一会话中得到稳定译文；
- 不重复翻译未变化的历史，降低延迟、费用和 1M 上下文场景的请求体压力；
- 翻译失败时安全停止转发；
- 上下文出现漂移、缓存丢失或版本不兼容时可回退到全量重建。

## 2. 范围与边界

仅处理 POST JSON 中 `input` 或 `messages` 里的 user 文本：包括 Responses 的字符串 `input`、数组中的裸字符串条目，以及 `role=user` 消息的 `content` 文本部分。`instructions`、工具输出、图片、文件和非 user 消息保持原样，但任何“像 user 文本却不认识的形态”只要含汉字就 fail-closed 拒绝，绝不静默透传。会话由上游请求中的稳定 conversation/thread/previous_response_id 标识识别；无法识别标识时按请求级临时会话全量处理。

网关不把会话快照（上一轮 `input`/`messages` 前缀）写入磁盘，也不记录
User Prompt 或密钥。Translate Cache 只把「原文 hash → Translated Prompt」
写到本机文件；进程重启后会话前缀失效，但相同原文仍命中译文，不必再打
Translator Backend。

## 3. 数据模型

```text
Session {
  id              string
  generation      uint64
  messages        []MessageSnapshot
  translated      map[string]TranslatedPart
  lastSeen        time.Time
  requestCount    uint64
}

MessageSnapshot {
  fingerprint     string   // role、位置、非文本字段和原文的稳定哈希
  userTextParts   []TextPart
}

TranslatedPart {
  sourceHash      string
  translatedText  string
  createdAt       time.Time
}
```

`sourceHash` 必须包含会影响翻译结果的文本和翻译策略版本；更换提示词或模型时自动失效。

## 4. 请求处理流程

1. 解析请求并提取会话 ID；解析失败直接返回 403。
2. 对消息列表计算结构指纹，并与会话快照比较。
3. 若为新会话，或消息列表不是旧快照的前缀扩展，执行一次全量翻译并创建新 generation。
4. 若为前缀扩展，只对新增 user 文本调用 Translator Backend；未变化部分从 `translated` 读取。
5. 将缓存译文和未改写内容按原始顺序组装，追加 `Please reply in Chinese.`，转发到上游。
6. 上游成功后提交快照；翻译或上游失败时不提交半成品状态。

编辑、重试、分支会话、消息删除或顺序变化都视为非前缀变更，触发全量重建，避免错误复用译文。

## 5. 一致性与回退

- 每次增量组装后验证消息数量、顺序和每个文本部分的 fingerprint；验证失败立即丢弃本次结果并全量重建一次。
- 每 20 次请求或每 10 分钟执行一次后台一致性检查；检查只比较指纹，不重新发送正文。发现漂移则下次请求全量重建。
- Translator Backend 超时、返回格式非法或译文缺失时 fail-closed，返回 HTTP 403，不把未翻译中文放行。
- 每次翻译后回检译文：仍含汉字的译文丢弃，请求返回 HTTP 403，绝不转发。
- 转发前对组装后的完整请求再做一次零汉字断言（Han-free Invariant）：任何 user 文本位置的**叙述汉字**残留即 fail-closed。工作目录等文件路径里已有的汉字可以原样保留，不送 Translator Backend。
- 密钥样的行若含汉字，整行拒绝，不译也不转发。
- 全量重建失败时保留旧快照但不转发当前请求；连续失败次数写入运行状态，日志不得包含正文。

## 6. 缓存与资源限制

默认每会话最多保留 1000 条消息、32 MiB 原文与译文；超过限制时淘汰最旧会话。会话空闲 30 分钟自动过期。会话快照只在内存中，使用互斥保护，不能因并发请求产生两个 generation。Translate Cache 的键是 `sha256(strategyVersion + model + chunk)`，可持久化；全量重建也查这份缓存。模型或策略版本变化会使相关条目失效。

## 7. 可观测性

Control Page 显示：当前会话数、缓存命中率、增量翻译字符数、全量回退次数、最近失败原因和当前 generation。日志只记录计数、耗时、哈希前缀和错误类型，不记录用户正文、Authorization 或 API key。

建议指标：`translate_incremental_total`、`translate_full_total`、`translate_cache_hits_total`、`translate_fallback_total`、`translate_failed_total`、`translate_latency_ms`。

## 8. 验收标准

- 首次请求翻译全部 user 文本；追加一条消息时只调用一次增量翻译。
- 重试完全相同的请求不再次调用 Translator Backend。
- 修改历史消息、删除消息、改变顺序会触发全量翻译。
- 任意翻译失败都不会把原始中文请求发送到上游。
- 并发请求不会覆盖较新的快照，generation 单调递增。
- 进程重启后不读取磁盘上下文，下一请求按新会话全量建立。
- 1M 上下文下历史内容不因每轮重复翻译而重复产生 Translator Backend 调用。
