# OpenCode Go translation API shape

Question: how to send a Chinese User Prompt to the OpenCode Go API and get an English translation — official base URL, auth header, request body shape, cheap/fast model ids, and where a key might live locally (names/paths only).

Date: 2026-09-08

## Answer (gist)

POST `https://opencode.ai/zen/go/v1/chat/completions` with `Authorization: Bearer <api-key>` and a Chat Completions body; cheapest non-training chat models on that route are `mimo-v2.5` and `glm-5.3-flash`.

## Scope and ownership

This note follows first-party OpenCode sources only: the Go/Zen docs on opencode.ai, the Go HTTP routes in the OpenCode console, models.dev provider metadata owned by OpenCode, and the OpenCode client auth/config docs. Third-party Docker/GoModel write-ups are not used.

OpenCode Go is a $10/month subscription accessed with an API key copied from the OpenCode Zen console after subscribing to Go. ([Go docs](https://opencode.ai/docs/go/))

The product is designed for OpenCode and other coding agents that send similar traffic. Official client requirements: identify the client with a non-generic `User-Agent` (example `my-coding-agent/1.0`) and send a stable session id in `x-opencode-session`. Traffic is monitored for abuse. ([Go docs — Where can I use it?](https://opencode.ai/docs/go/))

## Base URL

Official Go endpoints are under:

```
https://opencode.ai/zen/go/v1
```

That prefix is the `api` field in OpenCode’s own models.dev provider definition. ([models.dev `opencode-go/provider.toml`](https://github.com/sst/models.dev/blob/dev/providers/opencode-go/provider.toml))

The published model table then names the full path per model:

| Shape | Full URL | Typical models |
| --- | --- | --- |
| Chat Completions | `https://opencode.ai/zen/go/v1/chat/completions` | GLM Flash/5.x, Kimi, LongCat, DeepSeek, MiMo, Hy, Omen |
| Responses | `https://opencode.ai/zen/go/v1/responses` | Grok 4.6, GPT 5.6 Luna, Muse Spark Contributor |
| Anthropic Messages | `https://opencode.ai/zen/go/v1/messages` | MiniMax, Qwen 3.6–3.8 |

Source: [Go docs — Endpoints](https://opencode.ai/docs/go/).

Live catalog (no auth required on GET):

```
https://opencode.ai/zen/go/v1/models
```

Fetched 2026-09-08; `object` is `"list"`, each entry has `id`. ([Go docs — Models](https://opencode.ai/docs/go/); [GET handler](https://github.com/sst/opencode/blob/dev/packages/console/app/src/routes/zen/go/v1/models.ts))

In an OpenCode config, the model string is `opencode-go/<model-id>` (example `opencode-go/kimi-k3`). ([Go docs — Endpoints](https://opencode.ai/docs/go/))

## Auth header (by endpoint)

Go’s first-party HTTP routes parse the key differently per dialect. This is the owner of the contract.

**Chat Completions and Responses** take the second space-separated token of `Authorization` (the usual `Bearer <key>` form):

```ts
parseApiKey: (headers: Headers) => headers.get("authorization")?.split(" ")[1]
```

Sources: [`zen/go/v1/chat/completions.ts`](https://github.com/sst/opencode/blob/dev/packages/console/app/src/routes/zen/go/v1/chat/completions.ts), [`zen/go/v1/responses.ts`](https://github.com/sst/opencode/blob/dev/packages/console/app/src/routes/zen/go/v1/responses.ts).

**Anthropic Messages** takes `x-api-key` only:

```ts
parseApiKey: (headers: Headers) => headers.get("x-api-key") ?? undefined
```

Source: [`zen/go/v1/messages.ts`](https://github.com/sst/opencode/blob/dev/packages/console/app/src/routes/zen/go/v1/messages.ts).

For a Chat Completions translation call, send:

```
Authorization: Bearer <OpenCode API key>
```

The Go handler also reads (does not replace the API key) `x-opencode-session`, `x-opencode-request`, `x-opencode-client`, `x-opencode-project`, and `user-agent`. ([`handler.ts`](https://github.com/sst/opencode/blob/dev/packages/console/app/src/routes/zen/util/handler.ts); [Go docs client requirements](https://opencode.ai/docs/go/))

The OpenCode CLI itself, when the provider id starts with `opencode`, sets `x-opencode-session`, `x-opencode-request`, `x-opencode-client`, optional `x-opencode-project`, and a product `User-Agent`. ([`packages/opencode/src/session/llm/request.ts`](https://github.com/sst/opencode/blob/dev/packages/opencode/src/session/llm/request.ts))

## Request body shape

The Go endpoint table’s “AI SDK Package” column is the documented dialect:

- `@ai-sdk/openai-compatible` → Chat Completions (`/v1/chat/completions`)
- `@ai-sdk/openai` → Responses (`/v1/responses`)
- `@ai-sdk/anthropic` → Messages (`/v1/messages`)

([Go docs — Endpoints](https://opencode.ai/docs/go/); OpenCode providers docs: use `@ai-sdk/openai-compatible` for `/v1/chat/completions` and `@ai-sdk/openai` for `/v1/responses`. ([Providers](https://opencode.ai/docs/providers/)))

The console’s Chat Completions parser expects an OpenAI-style body and reads `body.model`. Fields it normalizes: `model`, `messages` (roles `system` / `user` / `assistant` / `tool`), `max_tokens`, `temperature`, `top_p`, `stop`, `stream`, `tools`, `tool_choice`. ([`fromOaCompatibleRequest`](https://github.com/sst/opencode/blob/dev/packages/console/app/src/routes/zen/util/provider/openai-compatible.ts))

Minimal Chat Completions body for a short translation:

```json
{
  "model": "mimo-v2.5",
  "messages": [
    { "role": "system", "content": "Translate the user text into English. Return only the translation." },
    { "role": "user", "content": "<Chinese User Prompt>" }
  ]
}
```

Responses models are a different shape: the console parser reads `body.model` and conversation items from `body.input` (falling back to `body.messages`), plus `max_output_tokens` / `max_tokens`. ([`fromOpenaiRequest`](https://github.com/sst/opencode/blob/dev/packages/console/app/src/routes/zen/util/provider/openai.ts); route `format: "openai"` in [`responses.ts`](https://github.com/sst/opencode/blob/dev/packages/console/app/src/routes/zen/go/v1/responses.ts))

Anthropic Messages models use `x-api-key` and a Messages body (`model` + Anthropic `messages`), not Chat Completions. Skip that dialect unless the chosen model is MiniMax or Qwen on Go.

**For short/fast/cheap translation, stay on Chat Completions.** Do not use `/v1/responses` unless the model table says that model lives there.

## Cheap / fast model ids

Prices and 5-hour request estimates are from the Go usage tables. “Cheaper models like MiMo-V2.5 allow for more requests.” ([Go docs — Usage limits](https://opencode.ai/docs/go/))

All ids below are Chat Completions unless noted. Config form is `opencode-go/<id>`.

| Model id | Go list price (input / output per 1M) | Est. requests / 5h | Notes |
| --- | --- | --- | --- |
| `mimo-v2.5` | $0.14 / $0.28 | 30,100 | Official cheap example; 0-day retention; Chat Completions |
| `glm-5.3-flash` | $0.15 / $0.50 | 1,580 | Flash SKU on the same Chat Completions URL |
| `hy3` | $0.14 / $0.58 | 4,300 | Chat Completions |
| `omen-alpha` | $0.20 / $0.66 | 11,600 | Chat Completions |
| `deepseek-v4-flash` | $0.22 / $0.66 off-peak | 7,600 | Peak hours cost more; Chat Completions |

Primary pick for this ticket: **`mimo-v2.5`**. Runner-up if Flash latency/quality is preferred: **`glm-5.3-flash`**.

Avoid for this use unless you explicitly want them:

- `muse-spark-1.2-contributor` / `muse-spark-1.3-contributor` — cheapest tokens ($0.10 / $0.20) but **Responses** API, training consent, limited regions. ([Go docs — Endpoints and Privacy](https://opencode.ai/docs/go/))
- `qwen3.8-flash` — cheap Flash pricing but **Messages** (`x-api-key`), not Chat Completions.
- `gpt-5.6-luna` — inexpensive among GPT SKUs but **Responses**.

Model ids were also present on `GET https://opencode.ai/zen/go/v1/models` on 2026-09-08 (`mimo-v2.5`, `glm-5.3-flash`, `hy3`, `omen-alpha`, `deepseek-v4-flash`). The published Go list can change. ([Go docs](https://opencode.ai/docs/go/))

## Where a key might live (names and paths only)

No secret values are copied here. On this machine at research time: no `OPENCODE_*` environment variables were set; `~/.local/share/opencode/auth.json` did not exist; `~/.config/opencode/` existed with only `plugins/`; no `opencode.json` / `opencode.jsonc` and no worktree `.env`. `~/.codex/config.toml` `experimental_bearer_token` and Codex `auth.json` secrets were skipped as instructed.

### Environment

| Name | Owner |
| --- | --- |
| `OPENCODE_API_KEY` | models.dev env for both `opencode` and `opencode-go` ([`opencode-go/provider.toml`](https://github.com/sst/models.dev/blob/dev/providers/opencode-go/provider.toml), [`opencode/provider.toml`](https://github.com/sst/models.dev/blob/dev/providers/opencode/provider.toml)). OpenCode loads `provider.env` and, when a single env name is set, uses that value as the provider `apiKey`. ([`provider.ts` env loop](https://github.com/sst/opencode/blob/dev/packages/opencode/src/provider/provider.ts)). Also shown in [ACP docs](https://opencode.ai/docs/acp/) as an example env to forward. |
| `OPENCODE_AUTH_CONTENT` | If set, OpenCode parses this JSON instead of reading `auth.json`. ([`packages/opencode/src/auth/index.ts`](https://github.com/sst/opencode/blob/dev/packages/opencode/src/auth/index.ts)) |
| `{env:VARIABLE_NAME}` in config | Generic substitution into `provider.<id>.options.apiKey`. ([Config — Env vars](https://opencode.ai/docs/config/)) |

The CLI env-var table does **not** list `OPENCODE_API_KEY`; that name comes from the provider catalog, not the CLI flag list. ([CLI — Environment variables](https://opencode.ai/docs/cli/))

CLI startup also loads keys from “your environments or a `.env` file in your project.” ([CLI — auth login](https://opencode.ai/docs/cli/))

### OpenCode credential file

`/connect` and `opencode auth login` store provider credentials in:

```
~/.local/share/opencode/auth.json
```

([Providers](https://opencode.ai/docs/providers/), [Troubleshooting — Storage](https://opencode.ai/docs/troubleshooting/), [CLI — auth](https://opencode.ai/docs/cli/))

Schema (field **names** only): top-level keys are provider ids. API-key entries use `type` (`"api"`), `key`, optional `metadata`. OAuth entries use `type` (`"oauth"`), `refresh`, `access`, `expires`, optional `accountId` / `enterpriseUrl`. Well-known entries use `type` (`"wellknown"`), `key`, `token`. ([`auth/index.ts`](https://github.com/sst/opencode/blob/dev/packages/opencode/src/auth/index.ts))

After TUI `/connect` → **OpenCode Go**, the provider id to look for is `opencode-go`. Zen uses `opencode`. ([Go docs](https://opencode.ai/docs/go/), [Providers — OpenCode Go](https://opencode.ai/docs/providers/))

### OpenCode config (optional `apiKey`)

Config files (later overrides earlier):

1. `~/.config/opencode/opencode.json` (or `.jsonc`)
2. `OPENCODE_CONFIG` path
3. project `opencode.json`
4. `OPENCODE_CONFIG_CONTENT`
5. older: `~/.local/share/opencode/opencode.jsonc`

([Config — Locations](https://opencode.ai/docs/config/), [Troubleshooting](https://opencode.ai/docs/troubleshooting/))

Relevant field names: `provider`, `opencode-go` (or `opencode`), `options`, `apiKey`, `headers`, `baseURL`. Example pattern from docs: `provider.<id>.options.apiKey` with `{env:…}` or `{file:~/.secrets/…}`. ([Config](https://opencode.ai/docs/config/), [Providers — custom provider](https://opencode.ai/docs/providers/))

## Recommended call for this ticket

1. Base URL / method: `POST https://opencode.ai/zen/go/v1/chat/completions`
2. Auth: `Authorization: Bearer <key>`
3. Body: Chat Completions (`model` + `messages`), not Responses
4. Model id: `mimo-v2.5` (fallback `glm-5.3-flash`)
5. Also send a real `User-Agent` and `x-opencode-session`
6. Key lookup: env `OPENCODE_API_KEY`, then `~/.local/share/opencode/auth.json` provider `opencode-go` field `key`, then `provider.opencode-go.options.apiKey` in OpenCode config

## Sources

- [OpenCode Go](https://opencode.ai/docs/go/) — subscription, endpoints, prices, client headers, config model id format
- [OpenCode Zen](https://opencode.ai/docs/zen/) — related gateway; same console for keys
- [Providers](https://opencode.ai/docs/providers/) — `/connect`, `auth.json`, SDK package vs endpoint dialect
- [Config](https://opencode.ai/docs/config/) — file paths, `apiKey`, `{env:}` / `{file:}`
- [CLI](https://opencode.ai/docs/cli/) — `auth.json`, project `.env`
- [Troubleshooting](https://opencode.ai/docs/troubleshooting/) — `~/.local/share/opencode/auth.json`
- [ACP](https://opencode.ai/docs/acp/) — `OPENCODE_API_KEY` name
- [models.dev `opencode-go` provider.toml](https://github.com/sst/models.dev/blob/dev/providers/opencode-go/provider.toml) — `env = ["OPENCODE_API_KEY"]`, `api = "https://opencode.ai/zen/go/v1"`
- OpenCode console Go routes: [chat/completions](https://github.com/sst/opencode/blob/dev/packages/console/app/src/routes/zen/go/v1/chat/completions.ts), [responses](https://github.com/sst/opencode/blob/dev/packages/console/app/src/routes/zen/go/v1/responses.ts), [messages](https://github.com/sst/opencode/blob/dev/packages/console/app/src/routes/zen/go/v1/messages.ts)
- Body parsers: [openai-compatible.ts](https://github.com/sst/opencode/blob/dev/packages/console/app/src/routes/zen/util/provider/openai-compatible.ts), [openai.ts](https://github.com/sst/opencode/blob/dev/packages/console/app/src/routes/zen/util/provider/openai.ts)
- Client auth: [auth/index.ts](https://github.com/sst/opencode/blob/dev/packages/opencode/src/auth/index.ts)
- `GET https://opencode.ai/zen/go/v1/models` (2026-09-08)
