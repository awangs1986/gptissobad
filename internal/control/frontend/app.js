const $ = (id) => document.getElementById(id);

const enabled = $("enabled");
const statusPill = $("status-pill");
const listenLine = $("listen-line");
const form = $("config-form");
const formMsg = $("form-msg");
const keyHint = $("key-hint");
const toml = $("toml");
const logs = $("logs");
const copyToml = $("copy-toml");
const testLogin = $("test-login");
const frontEnabled = $("front-enabled");
const frontHint = $("front-hint");

let lastSeq = 0;
let formLoaded = false;

function showMsg(text, bad) {
  formMsg.hidden = !text;
  formMsg.textContent = text;
  formMsg.classList.toggle("bad", Boolean(bad));
}

function applyState(state, fillForm) {
  enabled.checked = Boolean(state.enabled && state.running);
  if (fillForm) {
    $("upstream").value = state.upstream || "";
    $("gatewayPort").value = state.gatewayPort || "";
    $("model").value = state.model || "";
    $("fallbackModel").value = state.fallbackModel || "";
    $("frontPort").value = state.frontPort || "";
    $("basePath").value = state.basePath || "";
  }
  frontEnabled.checked = Boolean(state.frontEnabled);
  if (state.frontEnabled) {
    frontHint.textContent = state.frontReachable
      ? "前置在线：Codex 打前置，前置再转真网关。"
      : "前置未监听：看本页日志或 ~/.codex/logs/ensure.log。";
  } else {
    frontHint.textContent = "前置已关闭：Codex 直连真网关端口。";
  }
  toml.textContent = state.toml || "";
  const metrics = state.metrics || {};
  $("metric-requests").textContent = metrics.translationRequests || 0;
  $("metric-cache").textContent = metrics.cacheHits || 0;
  $("metric-rejected").textContent = metrics.rejected || 0;
  $("metric-incremental").textContent = metrics.incrementalRequests || 0;
  $("metric-full").textContent = metrics.fullRebuilds || 0;
  $("metric-sessions").textContent = metrics.activeSessions || 0;
  $("metric-latency").textContent = metrics.lastLatencyMs ? metrics.lastLatencyMs + " ms" : "—";
  $("metric-chars").textContent = metrics.translatedChars || 0;
  $("metric-prompt-tokens").textContent = metrics.promptTokens || 0;
  $("metric-completion-tokens").textContent = metrics.completionTokens || 0;
  $("metric-total-tokens").textContent = metrics.totalTokens || 0;
  $("metric-fallback").textContent = metrics.fallbackRequests || 0;
  if (state.credentialSource === "pi-login") {
    keyHint.textContent = "已复用 Pi 的 opencode-go 登录凭据（不会改写 Pi 文件）。";
  } else if (state.credentialSource === "environment") {
    keyHint.textContent = "已从 OPENCODE_API_KEY 读取凭据。";
  } else if (state.hasKey) {
    keyHint.textContent = "本机已保存密钥，输入新值才会覆盖。";
  } else {
    keyHint.textContent = "还没有密钥。可使用 Pi 的 opencode-go 登录、OPENCODE_API_KEY，或放进 ~/.codex/opencode-go.key。";
  }
  const basePath = state.basePath || "/v1";
  if (state.enabled && state.running) {
    statusPill.dataset.state = "on";
    statusPill.textContent = "运行中";
    if (state.frontEnabled && state.frontListen) {
      listenLine.textContent = "Codex 指向 http://" + state.frontListen + basePath + "（经前置）";
    } else {
      listenLine.textContent = "Codex 指向 http://" + state.listen + basePath;
    }
  } else if (state.error) {
    statusPill.dataset.state = "err";
    statusPill.textContent = "出错";
    listenLine.textContent = state.error;
  } else {
    statusPill.dataset.state = "off";
    statusPill.textContent = "未启用";
    listenLine.textContent = "先填上游地址，再打开开关。";
  }
}

async function api(path, options) {
  const res = await fetch(path, options);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error(data.error || "请求失败");
  }
  return data;
}

async function refresh() {
  applyState(await api("/api/state"), !formLoaded);
  formLoaded = true;
}

function appendLogs(lines) {
  for (const line of lines || []) {
    if (line.seq <= lastSeq) continue;
    lastSeq = line.seq;
    const item = document.createElement("li");
    const time = document.createElement("time");
    time.textContent = line.time;
    const msg = document.createElement("span");
    msg.textContent = line.msg;
    item.append(time, msg);
    logs.append(item);
  }
  logs.scrollTop = logs.scrollHeight;
}

enabled.addEventListener("change", async () => {
  try {
    applyState(await api("/api/enabled", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: enabled.checked }),
    }));
    showMsg("");
  } catch (err) {
    enabled.checked = false;
    showMsg(err.message, true);
    await refresh();
  }
});

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    applyState(await api("/api/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        upstream: $("upstream").value,
        gatewayPort: $("gatewayPort").value,
        model: $("model").value,
        fallbackModel: $("fallbackModel").value,
        apiKey: $("apiKey").value,
        frontPort: $("frontPort").value,
        basePath: $("basePath").value,
      }),
    }));
    $("apiKey").value = "";
    showMsg("已保存");
  } catch (err) {
    showMsg(err.message, true);
  }
});

testLogin.addEventListener("click", async () => {
  testLogin.disabled = true;
  showMsg("正在发送测试消息…");
  try {
    const result = await api("/api/test", { method: "POST" });
    showMsg(result.message || "登录有效");
  } catch (err) {
    showMsg(err.message, true);
  } finally {
    testLogin.disabled = false;
  }
});

frontEnabled.addEventListener("change", async () => {
  try {
    applyState(await api("/api/front", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: frontEnabled.checked }),
    }));
    showMsg("");
  } catch (err) {
    frontEnabled.checked = !frontEnabled.checked;
    showMsg(err.message, true);
    await refresh();
  }
});

copyToml.addEventListener("click", async () => {
  await navigator.clipboard.writeText(toml.textContent);
  copyToml.textContent = "已复制";
  setTimeout(() => { copyToml.textContent = "复制"; }, 1200);
});

async function startLogs() {
  try {
    const data = await api("/api/logs?after=0");
    appendLogs(data.lines);
  } catch {
    return;
  }
  const src = new EventSource("/api/logs/stream?after=" + lastSeq);
  src.onmessage = (event) => {
    appendLogs([JSON.parse(event.data)]);
  };
  src.onerror = () => {
    src.close();
    setTimeout(startLogs, 1500);
  };
}

refresh();
startLogs();
setInterval(refresh, 4000);
