# Codex prompt translate

A personal Codex plugin that turns a Chinese User Prompt into an English Translated Prompt via a Translator Backend, then attaches a Reply Instruction so Codex answers in Chinese. The reason it exists: this machine's Codex is unusable on Chinese input and works on English.

## Language

**User Prompt**:
The text the human submits to Codex on a turn.
_Avoid_: Codex's question, agent question, 问的问题 (when meaning the human's input)

**Translated Prompt**:
The English rendering of the User Prompt produced by the Translator Backend.
_Avoid_: 译文 (on its own: reads equally as the English sent to the model and as a translation shown back to the user)

**Reply Instruction**:
The fixed English sentence prepended to the first user text so Codex answers in Chinese, on translated and English turns alike. Canonical wording: `Please reply in Chinese.`
_Avoid_: 请用中文回复我 (as the payload sent to Codex)

**Authority Note**:
The sentence declaring that a Chinese user message and an accompanying English text are the same request and that the English one governs. Only meaningful when both renderings reach the model, i.e. the hook `additionalContext` Injection. Unused under Local Gateway replace, where the model sees the Translated Prompt plus non-user text forwarded as-is.
_Avoid_: 冲突声明, disclaimer

**Translate Cache**:
The map from a hash of source text (plus translator model and strategy version) to its Translated Prompt. Identical chunks reuse the same English. The hash→translation map may be written to disk; the User Prompt is never stored.
_Avoid_: session snapshot (the in-memory conversation prefix), writing conversation JSON, prompt log

**Translator Backend**:
The OpenCode Go API used only to produce a Translated Prompt. It is not Codex's coding model.
_Avoid_: OpenCode (the product), translator app, Codex backend

**Translate Gate**:
The rule that decides whether this User Prompt is sent to the Translator Backend. Canonical: send when the text contains a Han character; skip otherwise.

**Han-free Invariant** (historical; superseded by `docs/spec/translation-policy.md`):
The Local Gateway formerly guaranteed no Han prose upstream. Current policy is fail-open: only `role=user` and `role=assistant` content is translated (Han paths held out); everything else with Han forwards as-is. 403 happens only for quota exhaustion, Translator Backend failure (including Han-bearing translations), and unreachable Translator Backend. (Historical note: the guarantee used to be no Han prose upstream anywhere, enforced by translating, path hold-outs, per-translation re-checks, whole-body scans, and fail-closed on unrecognised shapes or undecodable bodies.)
_Avoid_: 防中文泄漏 (an effect, not the invariant itself)

**Injection**:
How the Translated Prompt reaches the model. Two mechanisms: hook `additionalContext` (original User Prompt stays on the turn) and local-gateway replace (user text is rewritten on the loopback hop before upstream).

**Local Gateway**:
The loopback rewrite proxy that produces the Translated Prompt and forwards the turn to the upstream. Codex does not call it directly when the Front Door is in use.
_Avoid_: MITM, TLS intercept, packet capture, Front Door

**Front Door**:
The loopback HTTP process Codex is configured to call. When the Local Gateway is not listening, it answers HTTP 403 instead of refusing the connection. It does not translate.
_Avoid_: 前置轻 Host (the implementation nickname), reverse proxy, health check

**Codex Plugin**:
An installable package with a `.codex-plugin/plugin.json` manifest and hooks, used on this machine only. SessionStart starts the Front Door and Watchdog; it does not touch the User Prompt.
_Avoid_: user hook file, marketplace listing, public plugin

**Control Page**:
The local web UI that turns the Local Gateway on or off, edits Translator Backend settings, and shows runtime logs. It never displays API keys after save.

**Watchdog**:
The desktop process that hosts the Control Page, can sit in the Linux Mint panel, and opens that page on click. Distributed as an AppImage.
_Avoid_: system service, Windows tray app
