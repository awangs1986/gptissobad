# Cockpit Codex API Service: intercept-and-replace vs additionalContext

Question: can a personal translator sit on the same pattern as Cockpit Tools’ **Codex API Service** (sidecar `cockpit-cliproxy` / vendored CLIProxyAPI) and **replace** Chinese user text with English **before** the request is sent to OpenAI/Codex upstream — intercept-and-replace, not Codex `UserPromptSubmit` `additionalContext`?

Date: 2026-09-09

Source pin: [jlcodes99/cockpit-tools](https://github.com/jlcodes99/cockpit-tools) `main` newest commit `eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7` (2026-09-08, Homebrew cask v1.3.46). Tree SHA `ff130447bde12f309ec93a1bf3551b4e842f7077`. Handoff blob `b801f7667cdc4f749a3b42bd5693aeb75df10338`.

This note does not implement a proxy, does not document TLS/packet intercept, does not copy secrets, and does not edit `~/.codex/hooks.json`.

## Decision

**Yes, as a supported config/profile redirect — not as a Cockpit-shipped user-text rewrite, and not as TLS intercept.**

Cockpit’s Codex API Service is a **user-consented local OpenAI-compatible gateway**. Activating it backs up the target Codex profile, then writes a managed `auth.json` + `config.toml` so Codex sends HTTP to `http://localhost:<port>/v1` instead of ChatGPT/OpenAI. The sidecar (`cockpit-cliproxy`) authenticates the local client key, rewrites **model ids**, and forwards to Codex OAuth or a configured provider. That hop is where intercept-and-replace of the **upstream JSON body** is feasible.

Cockpit does **not** already replace user-prompt text. Its body rewrite is `model` alias/visibility (`rewriteBodyModel`). CLIProxyAPI’s plugin `request.normalize` / `request.intercept_before` *can* replace a request `Body`, but Cockpit’s sidecar does not generate a `plugins` config and does not serve POST `/v1/responses` through the stock CLIProxyAPI HTTP server (it uses its own Gin relay + executor). A translator would **reuse the redirect pattern** and **write the language rewrite**.

This is the opposite of `UserPromptSubmit`: that hook runs **inside** Codex before HTTP and cannot replace the User Prompt ([codex-userpromptsubmit.md](./codex-userpromptsubmit.md)). A localhost gateway runs **after** Codex has already recorded the original prompt in the UI/session.

On **this Windows profile** (`C:\Users\example-user`): Cockpit Tools is installed (`cockpit-tools.exe` + `cockpit-cliproxy.exe` under `%LOCALAPPDATA%\Cockpit Tools`). There is **no** `%USERPROFILE%\.codex` (`config.toml` / `auth.json` / `hooks.json` missing). Codex is not currently pointed at this gateway here. Feasibility is architectural; it is not already wired on this account.

## Config/profile redirect vs packet/TLS intercept

| Kind | What it is | In scope? |
| --- | --- | --- |
| **Config/profile redirect** | Codex `model_provider` + `base_url` + local bearer; client opens plaintext HTTP to loopback. Cockpit’s product feature. | Yes. This is the pattern to reuse. |
| **Packet/TLS intercept** | Catching traffic to `chatgpt.com` / `api.openai.com` without Codex choosing localhost (MITM, TLS-strip, forced system proxy onto OpenAI). | **Out of scope.** Not documented. Not a Cockpit feature. |

Cockpit goes out of its way to keep the client→sidecar hop on loopback and **out** of local proxy stacks: generated client Base URL uses host `localhost` (not `127.0.0.1`); launched Codex processes get `NO_PROXY` / `no_proxy` entries for loopback. That is product hardening against *accidental* proxy capture of the local hop, not a recipe for intercepting upstream TLS.

## Architecture: who listens, how Codex is pointed at localhost

Owner: [docs/CODEX_API_SERVICE_HANDOFF.md](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/docs/CODEX_API_SERVICE_HANDOFF.md).

```text
Codex CLI / selected Codex profile
  -> auth.json + config.toml injected by Cockpit
  -> http://localhost:<configured-port>/v1
  -> cockpit-cliproxy sidecar (default / only live mode)
  -> selected Codex OAuth credentials or configured provider API key
  -> OpenAI Codex upstream or provider upstream

React page -> Tauri invoke -> Rust collection/runtime -> sidecar config + manifest
```

UI name: **Codex API Service** (`src/pages/CodexApiServicePage.tsx`). Rust coordinator: `src-tauri/src/modules/codex_local_access.rs` (include-split). Sidecar binary name: `cockpit-cliproxy`.

### Who listens

- Bind host: loopback `127.0.0.1` (`CODEX_LOCAL_ACCESS_LOCALHOST_BIND_HOST`). User may choose LAN `0.0.0.0` (still requires the generated client API key).
- Client Base URL host: `localhost` by default (`CODEX_LOCAL_ACCESS_DEFAULT_CLIENT_URL_HOST`), optional `127.0.0.1`. URL shape: `http://localhost:<port>/v1`.
- Sidecar `main` requires `--config` and `--manifest`, loads CLIProxyAPI `config.LoadConfig`, then serves **its own** Gin router (`relayServer.router`) via `net.Listen` on `cfg.Host`/`cfg.Port`. It also starts a CLIProxyAPI `cliproxy.Service` runtime (`newSidecarRuntime`) for executors/auth — that is not the client-facing HTTP mux.
- Parent-process monitor: sidecar exits when Cockpit’s PID dies.

Handoff still describes two gateway modes (`sidecar` default, `legacy` in-process Rust HTTP). **Current `main` retires legacy at runtime:** `sanitize_collection_structure` comments “The legacy gateway is retired” and calls `migrate_legacy_gateway_mode`; `collection_gateway_mode` maps **both** `Legacy` and `Sidecar` to `Sidecar`. Leftover `[legacy]` listen code still exists in the same module; live startup goes through `prepare_sidecar_launch_config` + sidecar process.

Sidecar config artifacts (app data, not Codex home): `codex_local_access.json`, `codex_local_access_sidecar/` (`config.json`, `manifest.json`, `auths/`, quota-reserve). Handoff: inspect generated manifest **only locally**; it can contain credentials. This note does not quote those files.

### How Codex is pointed at localhost (product takeover)

Activation: `write_local_access_profile_takeover` in `codex_local_access_sidecar_runtime.rs`:

1. Builds a runtime API-key account whose `api_base_url` is `http://localhost:<port>/v1`, provider id `codex_local_access`, `api_wire_api = "responses"`.
2. Calls `codex_account::write_account_bundle_to_dir` (writes `auth.json` + provider block in `config.toml`).
3. Optionally sets `experimental_realtime_ws_base_url` to the same base URL for mixed-model Live/Realtime (only if the user has not already set it).
4. Writes a managed model catalog file and `model_catalog_json` in `config.toml`.

Takeover **inspection** (`codex_local_access_profile_takeover.rs`) treats a profile as attached when:

- `model_provider = "codex_local_access"`
- `[model_providers.codex_local_access].base_url` matches the collection URL
- `wire_api = "responses"`
- `experimental_bearer_token` equals the generated local API key
- `requires_openai_auth` / image-gen actor headers match OAuth-bound vs pure API-key mode

`auth.json` for the API-key takeover is the official Codex API-key shape: `auth_mode` plus `OPENAI_API_KEY` (the **local** client key, not the upstream ChatGPT token). Takeover backups live in Cockpit’s `codex_local_access_takeover_backups.json`. Disable restores the backed-up profile and strips owned fields (`experimental_bearer_token`, managed headers, managed `experimental_realtime_ws_base_url`). Changelog: disabling restores managed profile files and does not silently re-enable.

Default `~/.codex` is taken over only if that instance is **bound** to API Service (`collect_local_access_profile_takeover_dirs`). Cockpit-launched instances use their own profile dirs.

That is how official Codex (CLI or a Cockpit-managed desktop/app-server instance) is made to speak HTTP to localhost. It is not how an unmodified ChatGPT desktop session talking to `https://chatgpt.com/backend-api/codex` is captured.

## HTTP routes that carry the user prompt

### Client → sidecar (what Codex hits after takeover)

Sidecar Gin router (`sidecars/cockpit-cliproxy/relay_server.go` `router()`):

| Method | Path | Handler | User text? |
| --- | --- | --- | --- |
| POST | `/v1/responses` | `handleResponses` → executor `FormatOpenAIResponse` | **Yes** — primary Codex `wire_api = "responses"` path |
| POST | `/v1/responses/compact` | compact executor | compaction transcript, not a fresh User Prompt |
| GET | `/v1/responses` | `ResponsesWebsocket` (SDK) | **Yes, later** — prompt is in WebSocket frames, not the GET body |
| POST | `/v1/chat/completions` | executor `FormatOpenAI` | **Yes** if some client uses Chat Completions |
| POST | `/v1/chat/completions/v1/responses` | same as `/v1/responses` | compatibility |
| POST | `/v1/images/generations`, `/v1/images/edits` | image relay | `prompt` (image caption, not the coding User Prompt) |
| POST | `/v1/alpha/search` and `/backend-api/codex/alpha/search` | Codex `web.run` search | search payload, not the turn prompt |
| GET | `/v1/models` | catalog | no |
| POST/GET | `/v1/live`, `/v1/realtime/*` | Codex Live/Realtime | voice/realtime; middleware **skips** JSON inspect |

**Not registered** on the sidecar router: `POST/GET /backend-api/codex/responses`. Handoff still lists `/backend-api/codex/*` as a client contract the sidecar should preserve. Stock CLIProxyAPI `internal/api/server_routes.go` **does** register that group. Cockpit’s sidecar only exposes `/backend-api/codex/alpha/search` from that prefix. After takeover this is usually irrelevant: Codex’s `base_url` is `…/v1`, so it calls `/v1/responses`, not `/backend-api/codex/responses`.

The **legacy** Rust proxy (`codex_local_access_http.rs`) still accepts `/v1` **or** `/backend-api/codex` and maps them onto upstream `https://chatgpt.com/backend-api/codex`. That path is retired at runtime.

### Sidecar → upstream (not the client contract)

OAuth/Codex executor upstream base: `https://chatgpt.com/backend-api/codex` (`UPSTREAM_CODEX_BASE_URL`). API-key accounts use their configured provider base (must not be the local gateway URL). A translator that rewrites the **client** JSON before `Execute`/`ExecuteStream` sits on the sidecar inbound side.

## Where the user-text fields live

### POST `/v1/responses` (Codex after takeover)

JSON object. User-visible turn text is **not** a top-level `prompt` (that name is the image-generation field).

Typical fields the gateway already knows about:

- `model` — rewritten by Cockpit (`rewriteBodyModel` / `rewrite_request_model_alias_value`)
- `input` — array of items. User messages: `{ "type": "message", "role": "user", "content": [ { "type": "input_text", "text": "…" } ] }` (and `input_image` / `input_file` parts). Chat→Responses conversion in the **legacy** transformer emits exactly that shape from `messages[]`.
- `instructions` — developer/system instructions. **Not** the User Prompt. Changelog: empty/`null`/blank instructions are filled with the model’s official base instructions. Do not treat this as the Chinese user text to replace.
- `previous_response_id` — history may live **upstream**; then `input` is incremental (new user item + tool outputs), not the full transcript.
- `stream` — SSE when true (sidecar `handleStream`)
- `tools` / `tool_choice` / `reasoning` / `service_tier` — not user prose

WebSocket (`GET /v1/responses` upgrade): frames `response.create` / `response.append` carry the same `input` array (and may omit `instructions`, which the SDK copies from the last request). `shouldInspectJSONBody` is POST/PUT/PATCH only, so **policy middleware does not see WS prompt text**.

### POST `/v1/chat/completions`

- `messages[]`: `role` + `content` as string **or** parts (`text`, `image_url`, …). `role: "system"` is not the User Prompt; last `role: "user"` usually is.
- Legacy Rust converter maps `system` → Responses `role: "developer"` + `input_text`.
- Completions dialect (CLIProxyAPI `/v1/completions`): `prompt` — sidecar does not register `/v1/completions`.

### `/backend-api/codex`

Same Responses JSON as `/v1/responses` when a client uses that prefix. Official ChatGPT Codex without takeover talks to `chatgpt.com` on this path (OAuth), which this pattern does not see.

## Does the sidecar already have a rewrite hook for user text?

**No user-text rewrite. Yes, a JSON-body hook that only changes `model`.**

Inbound middleware (`requestPolicy.middleware` in `manifest_policy.go`):

1. Resolves the local API key.
2. For POST JSON (not Live/Realtime, not empty body): `readAndRestoreBody` → `rewriteBodyModel` → restore body.
3. `rewriteBodyModel` unmarshals JSON, reads `payload["model"]`, applies alias/visibility, maybe writes a new body. It does not walk `input` / `messages` / `text`.

`handleExecutorBody` then runs the CLIProxyAPI executor (stream or not). Protocol “translator” here means OpenAI/Claude/Gemini/Codex **schema** conversion, not natural language.

CLIProxyAPI **first-party** plugin surface (vendored `third_party/CLIProxyAPI`, module `v7.2.155`, sync note `sidecars/cockpit-cliproxy/UPSTREAM.md`):

- `request.normalize` (example `examples/plugin/request-normalizer`) returns a replacement `Body`.
- `request.intercept_before` (example `examples/plugin/request-lifecycle`) can terminate or, in the SDK interceptor host, replace `Body` before auth (`handlers_interceptors.go` copies `resp.Body` onto `req.Payload`).
- `request-translator` is **format** translation (OpenAI↔provider), not ZH→EN.

Cockpit does **not** emit a `plugins:` block in generated sidecar config (no matches in `codex_local_access_sidecar_config.rs`). Sidecar `main` never registers a CLIProxyAPI plugin host on the Gin relay. POST `/v1/responses` does not go through `OpenAIResponsesAPIHandler.Responses`; only the WebSocket GET uses that SDK handler. Dropping a CLIProxyAPI `.dll` next to Cockpit will not rewrite Codex turns.

## Feasibility on this machine’s Codex

| Fact | Observation |
| --- | --- |
| Cockpit Tools | Installed: `%LOCALAPPDATA%\Cockpit Tools\cockpit-tools.exe`, `cockpit-cliproxy.exe` |
| Codex home on this Windows user | **Absent:** no `C:\Users\example-user\.codex` |
| `hooks.json` | Not present here (not edited) |
| Sibling notes in this repo | Linux Codex CLI `0.153.4` / desktop `26.901.51231`, `[features] hooks = true`, trusted `SessionStart` in `~/.codex/hooks.json` ([codex-userpromptsubmit.md](./codex-userpromptsubmit.md), [local-plugin-install.md](./local-plugin-install.md)) |

So:

- **On this Windows profile:** there is no Codex client config to redirect. Installing/using Cockpit’s Codex API Service (or an equivalent local `/v1` provider) would be a new setup.
- **On a machine that already runs Codex 0.153.4** (the Linux setup in the sibling notes): `UserPromptSubmit` still cannot replace the User Prompt. Sitting on Cockpit’s pattern **can** replace the **upstream-visible** user text if that Codex profile is taken over (or manually given `model_provider` + `base_url` + local key) and a rewrite is added on POST `/v1/responses` `input`. The ChatGPT desktop app **without** that takeover still goes to ChatGPT directly and is not this hop.

Hooks still run first: SessionStart / UserPromptSubmit see the original Chinese. HTTP rewrite does not change hook IO. SessionStart handlers in `~/.codex/hooks.json` are not replaced by a plugin; takeover of `config.toml`/`auth.json` can still **break sessions** if the local gateway is down or the bearer does not match.

## Reuse vs write ourselves

**Reuse (pattern / optional code, not a drop-in):**

- Cockpit’s product redirect: local `base_url`, `wire_api = "responses"`, generated client key, backup/restore of profile files.
- Sidecar already reads and restores the POST JSON body (`readAndRestoreBody` / `rewriteBodyModel`). A language rewrite belongs next to that function, plus the same walk in the WebSocket `input` merger if WS is enabled.
- Field map: Responses `input[].content[].text` for `input_text`; Chat Completions `messages[].content`.
- CLIProxyAPI plugin APIs as a **design reference** if we ran our own CLIProxyAPI with `plugins.enabled` — not as something Cockpit loads today.
- Translation HTTP: OpenCode Go Chat Completions ([opencode-go-translate-api.md](./opencode-go-translate-api.md)), called from the rewrite, not from a Codex hook.

**Write ourselves:**

- Detect Chinese, call a translator, replace **only** the current user message (or all user `input_text` — product choice). Leave `instructions`, tool results, and images alone.
- Handle `previous_response_id` (incremental `input`) vs full transcript.
- SSE streaming: rewrite the **request** only; do not parse the token stream for user text.
- WebSocket: rewrite `response.create` / `response.append` `input` if `responses_websockets_enabled` is on (default off for new collections; changelog).
- Desktop vs CLI coverage: only profiles actually taken over (or equivalent `config.toml`).
- If we refuse to patch Cockpit: a **separate** loopback gateway plus the same official Codex `model_providers` redirect — still config redirect, still not TLS intercept. That is a new service, not a Cockpit plugin.

Do not reuse Cockpit’s `auth.json` rewrite casually: it is the high-risk part of the pattern.

## Risks

- **`auth.json` / `config.toml` rewrite.** Cockpit backups and restores; a homemade takeover can strand ChatGPT login, collide with Token Authority refresh (`refresh_owner: "cockpit_token_authority"` on sidecar auths), or leave `experimental_bearer_token` pointing at a dead local key. Disable/restore must be first-class.
- **Desktop vs CLI.** Takeover is per profile directory. Unmanaged ChatGPT desktop keeps talking to `chatgpt.com`. CLI with `model_provider = openai` likewise never hits localhost.
- **Streaming SSE.** User text is in the POST body; responses are SSE. Rewrite before `handleStream`. Failures after headers are committed become SSE `error` events (`immediateSseResponse` in the handoff).
- **WebSocket.** Prompt not in GET; middleware skips it. If the user enables Responses WebSocket, HTTP-only rewrite misses turns.
- **Live/Realtime.** Middleware skips `/v1/live` and `/v1/realtime/*`. Voice/realtime user audio/text is a different path.
- **Existing SessionStart hooks.** They still run. They are not uninstalled by API Service. A broken takeover can prevent Codex from starting, so those hooks never fire; that is an availability risk, not a hook-schema conflict. `UserPromptSubmit` (if added later) still sees Chinese.
- **Over-rewrite.** Translating `instructions`, tool JSON, or prior English history will damage the turn. Compact and `web.run` search are different payloads.
- **Latency / double model call.** Each turn waits on the translator before Codex upstream.
- **Cockpit upgrades.** Patching `rewriteBodyModel` inside a bundled `cockpit-cliproxy.exe` is wiped on update unless we own the sidecar.

## Gist (for a wayfinder ticket)

Cockpit Codex API Service is a **consented localhost OpenAI-compatible gateway** (sidecar `cockpit-cliproxy`, CLIProxyAPI v7.2.155) that **redirects a Codex profile** via `auth.json` + `config.toml` (`model_provider = codex_local_access`, `base_url = http://localhost:<port>/v1`, `wire_api = responses`). That is the supported intercept-and-replace hop: POST `/v1/responses` JSON `input[]` user `input_text` (and optionally Chat Completions `messages[]`) **before** the executor calls `chatgpt.com/backend-api/codex`. Cockpit does **not** rewrite user language today (`rewriteBodyModel` only changes `model`); its sidecar does **not** load CLIProxyAPI body plugins. TLS/packet intercept of OpenAI is out of scope. On this Windows user there is no `~/.codex` to redirect; on the Linux Codex 0.153.4 described in sibling notes, this pattern can replace **upstream** user text while UI/hooks keep the original Chinese — unlike `UserPromptSubmit`, which cannot replace the User Prompt. Reuse the redirect and the POST-body middleware site; write the ZH→EN field walk (and WS `input` if websockets are on). Do not casually rewrite `auth.json`.

## Sources

- [jlcodes99/cockpit-tools `main` @ eedfe0c](https://github.com/jlcodes99/cockpit-tools/commit/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7)
- [docs/CODEX_API_SERVICE_HANDOFF.md](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/docs/CODEX_API_SERVICE_HANDOFF.md) — UI name, flow, routes, takeover of `auth.json`/`config.toml`, sidecar vs legacy, bind/client hosts
- [CHANGELOG.md](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/CHANGELOG.md) — sidecar-only migration, `localhost` client URL, loopback `NO_PROXY`, WS off by default, instructions fill, disable restores profiles
- Sidecar: [`sidecars/cockpit-cliproxy/main.go`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/main.go), [`relay_server.go`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/relay_server.go), [`manifest_policy.go`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/manifest_policy.go) (`rewriteBodyModel`, policy middleware), [`relay_execution.go`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/relay_execution.go), [`provider_gateway.go`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/provider_gateway.go) (`handleExecutorRequest`), [`runtime_auth.go`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/runtime_auth.go) (`newSidecarRuntime`), [`UPSTREAM.md`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/UPSTREAM.md), [`go.mod`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/go.mod) (`CLIProxyAPI/v7 v7.2.155`, `replace => ./third_party/CLIProxyAPI`)
- Rust: [`codex_local_access.rs`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/src-tauri/src/modules/codex_local_access.rs), [`codex_local_access_foundation.rs`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/src-tauri/src/modules/codex_local_access_foundation.rs) (paths, `UPSTREAM_CODEX_BASE_URL`), [`codex_local_access_profile_takeover.rs`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/src-tauri/src/modules/codex_local_access_profile_takeover.rs), [`codex_local_access_sidecar_runtime.rs`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/src-tauri/src/modules/codex_local_access_sidecar_runtime.rs) (`write_local_access_profile_takeover`), [`codex_local_access_request_transform.rs`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/src-tauri/src/modules/codex_local_access_request_transform.rs) (chat `messages` → Responses `input`), [`codex_local_access_http.rs`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/src-tauri/src/modules/codex_local_access_http.rs) (legacy `/v1` + `/backend-api/codex`), [`codex_local_access_collection.rs`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/src-tauri/src/modules/codex_local_access_collection.rs) (legacy retired), [`codex_account_projection.rs`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/src-tauri/src/modules/codex_account_projection.rs) (`auth.json` field names), [`models/codex_local_access.rs`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/src-tauri/src/models/codex_local_access.rs) (`GatewayMode`)
- CLIProxyAPI (vendored, first-party dependency): [`internal/api/server_routes.go`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/third_party/CLIProxyAPI/internal/api/server_routes.go), [`sdk/api/handlers/openai/openai_handlers.go`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/third_party/CLIProxyAPI/sdk/api/handlers/openai/openai_handlers.go), [`openai_responses_handlers.go`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/third_party/CLIProxyAPI/sdk/api/handlers/openai/openai_responses_handlers.go), [`openai_responses_websocket_requests.go`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/third_party/CLIProxyAPI/sdk/api/handlers/openai/openai_responses_websocket_requests.go), [`handlers_interceptors.go`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/third_party/CLIProxyAPI/sdk/api/handlers/handlers_interceptors.go), [`examples/plugin/README.md`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/third_party/CLIProxyAPI/examples/plugin/README.md), [`examples/plugin/request-normalizer/go/main.go`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/third_party/CLIProxyAPI/examples/plugin/request-normalizer/go/main.go), [`examples/plugin/request-lifecycle/README.md`](https://github.com/jlcodes99/cockpit-tools/blob/eedfe0c8eef8dbc68dff1828600a65f5e5ddb2c7/sidecars/cockpit-cliproxy/third_party/CLIProxyAPI/examples/plugin/request-lifecycle/README.md)
- This machine (paths only): `%LOCALAPPDATA%\Cockpit Tools`; no `%USERPROFILE%\.codex`
- Sibling notes: [codex-userpromptsubmit.md](./codex-userpromptsubmit.md), [opencode-go-translate-api.md](./opencode-go-translate-api.md), [local-plugin-install.md](./local-plugin-install.md)
