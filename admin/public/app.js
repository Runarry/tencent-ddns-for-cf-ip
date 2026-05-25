const defaultSpeedPresets = [
  { name: "Cloudflare 1MB", url: "https://speed.cloudflare.com/__down?bytes=1048576" },
  { name: "Cloudflare 10MB", url: "https://speed.cloudflare.com/__down?bytes=10485760" },
  { name: "Cloudflare 50MB", url: "https://speed.cloudflare.com/__down?bytes=52428800" },
  { name: "Cloudflare 100MB", url: "https://speed.cloudflare.com/__down?bytes=104857600" },
];

const state = {
  status: null,
  records: [],
  subscriptions: [],
  speedPresets: defaultSpeedPresets,
  temporarySpeedTest: null,
  activeTab: "overview",
};

const $ = (id) => document.getElementById(id);

const loginView = $("loginView");
const adminView = $("adminView");
const toast = $("toast");

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: {
      "content-type": "application/json",
      ...(options.headers || {}),
    },
  });
  if (!response.ok) {
    let message = response.statusText;
    try {
      const body = await response.json();
      message = body.error || message;
    } catch {
      message = await response.text();
    }
    throw new Error(message || `HTTP ${response.status}`);
  }
  if (response.status === 204) {
    return null;
  }
  return response.json();
}

async function boot() {
  bindEvents();
  try {
    await api("/session");
    showAdmin();
    await refreshAll();
  } catch {
    showLogin();
  }
}

function bindEvents() {
  $("loginForm").addEventListener("submit", async (event) => {
    event.preventDefault();
    $("loginError").textContent = "";
    try {
      await api("/login", {
        method: "POST",
        body: JSON.stringify({ password: $("passwordInput").value }),
      });
      $("passwordInput").value = "";
      showAdmin();
      await refreshAll();
    } catch (error) {
      $("loginError").textContent = error.message;
    }
  });

  $("logoutButton").addEventListener("click", async () => {
    await api("/logout", { method: "POST" }).catch(() => null);
    showLogin();
  });

  $("refreshButton").addEventListener("click", refreshAll);
  $("updateButton").addEventListener("click", runUpdate);
  $("newSubscriptionButton").addEventListener("click", () => openSubscriptionDialog());
  $("saveSubscriptionButton").addEventListener("click", saveSubscription);
  $("deleteSubscriptionButton").addEventListener("click", deleteSubscription);
  $("rotateKeyButton").addEventListener("click", rotateKey);
  $("runSpeedTestButton").addEventListener("click", runTemporarySpeedTest);
  $("applySpeedTestButton").addEventListener("click", () => applyTemporarySpeedTest());
  $("speedPresetSelect").addEventListener("change", () => {
    const url = $("speedPresetSelect").value;
    if (url) {
      $("speedTestUrl").value = url;
    }
  });

  document.querySelectorAll(".tab").forEach((button) => {
    button.addEventListener("click", () => setTab(button.dataset.tab));
  });
}

function showLogin() {
  loginView.hidden = false;
  adminView.hidden = true;
}

function showAdmin() {
  loginView.hidden = true;
  adminView.hidden = false;
}

async function refreshAll() {
  setBusy(true);
  try {
    const [status, records, subscriptions, speedPresets] = await Promise.all([
      api("/api/v1/status"),
      api("/api/v1/records"),
      api("/api/v1/admin/subscriptions"),
      api("/api/v1/admin/speed-test-presets").catch(() => ({ presets: defaultSpeedPresets })),
    ]);
    state.status = status;
    state.records = records.records || [];
    state.subscriptions = subscriptions.subscriptions || [];
    state.speedPresets = speedPresets.presets?.length ? speedPresets.presets : defaultSpeedPresets;
    render();
  } catch (error) {
    notify(error.message);
    if (error.message === "unauthorized") {
      showLogin();
    }
  } finally {
    setBusy(false);
  }
}

async function runUpdate() {
  setBusy(true);
  try {
    await api("/api/v1/update", { method: "POST" });
    notify("同步完成");
    await refreshAll();
  } catch (error) {
    notify(error.message);
  } finally {
    setBusy(false);
  }
}

function render() {
  const snapshot = state.status?.state?.last_sync;
  $("syncState").textContent = state.status?.running ? "同步运行中" : "服务空闲";
  $("recordCount").textContent = state.records.length.toString();
  $("subscriptionCount").textContent = state.subscriptions.length.toString();
  $("nextRun").textContent = formatDate(state.status?.next_run_at || state.status?.state?.next_run_at);
  $("lastResult").textContent = snapshot ? (snapshot.success ? "成功" : "失败") : "-";
  renderRecentRecords();
  renderSubscriptions();
  renderRecords();
  renderSpeedPresets();
  renderTemporarySpeed();
  renderSpeed();
}

function renderRecentRecords() {
  const records = [...state.records]
    .filter((record) => record.nodeid !== "fallback")
    .sort((a, b) => (b.speed_bps || 0) - (a.speed_bps || 0) || (a.latency_ms || 0) - (b.latency_ms || 0))
    .slice(0, 5);
  $("recentRecords").innerHTML = records.map(recordItem).join("") || empty("暂无优选记录");
}

function renderSubscriptions() {
  $("subscriptionList").innerHTML =
    state.subscriptions
      .map((sub) => `
        <article class="item">
          <div class="item-head">
            <div class="item-title">
              <strong>${escapeHTML(sub.name || "未命名订阅")}</strong>
              <div class="mono">${escapeHTML(sub.url_template || "")}</div>
            </div>
            <span class="badge">${sub.enabled ? "启用" : "停用"} · ${sub.editable ? "可编辑" : "配置只读"}</span>
          </div>
          <small>${escapeHTML((sub.nodeids || []).join(", ") || "全部线路")} · ${sub.share_count || 0} 条分享</small>
          <div class="actions">
            <button class="secondary" type="button" data-copy="${escapeAttr(sub.url_template || "")}">复制地址</button>
            <button class="secondary" type="button" data-edit="${escapeAttr(sub.id)}" ${sub.editable ? "" : "disabled"}>编辑</button>
          </div>
        </article>
      `)
      .join("") || empty("暂无订阅");

  $("subscriptionList").querySelectorAll("[data-edit]").forEach((button) => {
    button.addEventListener("click", () => {
      const sub = state.subscriptions.find((item) => item.id === button.dataset.edit);
      openSubscriptionDialog(sub);
    });
  });
  $("subscriptionList").querySelectorAll("[data-copy]").forEach((button) => {
    button.addEventListener("click", async () => {
      await navigator.clipboard.writeText(button.dataset.copy);
      notify("已复制");
    });
  });
}

function renderRecords() {
  $("recordsTable").innerHTML =
    state.records
      .map((record) => `
        <tr>
          <td>${escapeHTML(record.nodeid || "-")}</td>
          <td>${escapeHTML(record.fqdn || record.name || "-")}</td>
          <td>${escapeHTML(record.value || "-")}</td>
          <td>${formatLatency(record.latency_ms)}</td>
          <td>${formatDate(record.updated_at)}</td>
        </tr>
      `)
      .join("") || `<tr><td colspan="5">暂无记录</td></tr>`;
}

function renderSpeed() {
  $("speedTable").innerHTML =
    state.records
      .filter((record) => record.nodeid !== "fallback")
      .map((record) => `
        <tr>
          <td>${escapeHTML(record.nodeid || "-")}</td>
          <td>${escapeHTML(record.fqdn || record.name || "-")}</td>
          <td>${formatSpeed(record.speed_bps)}</td>
          <td>${formatLatency(record.ttfb_ms)}</td>
          <td>${formatBytes(record.download_bytes)}</td>
          <td>${formatLatency(record.download_ms)}</td>
        </tr>
      `)
      .join("") || `<tr><td colspan="6">暂无测速结果</td></tr>`;
}

function renderSpeedPresets() {
  const select = $("speedPresetSelect");
  if (select.dataset.loaded === "true" && select.options.length > 0) {
    return;
  }
  select.innerHTML = state.speedPresets
    .map((preset) => `<option value="${escapeAttr(preset.url)}">${escapeHTML(preset.name)}</option>`)
    .join("");
  const defaultPreset = state.speedPresets.find((preset) => preset.name.includes("10MB")) || state.speedPresets[0];
  if (defaultPreset) {
    select.value = defaultPreset.url;
    $("speedTestUrl").value = defaultPreset.url;
  }
  select.dataset.loaded = "true";
}

function renderTemporarySpeed() {
  const test = state.temporarySpeedTest;
  $("applySpeedTestButton").hidden = !test;
  $("temporarySpeedWrap").hidden = !test;
  if (!test) {
    $("temporarySpeedTable").innerHTML = "";
    return;
  }
  $("speedTestStatus").textContent = `临时测速完成：${formatDate(test.ended_at)} · ${test.url}`;
  $("temporarySpeedTable").innerHTML =
    (test.results || [])
      .map((result) => `
        <tr>
          <td>${escapeHTML(result.nodeid || "-")}</td>
          <td>${escapeHTML(result.fqdn || result.name || "-")}</td>
          <td>${escapeHTML(result.ip || "-")}</td>
          <td>${formatSpeed(result.speed_bps)}</td>
          <td>${formatLatency(result.ttfb_ms)}</td>
          <td>${formatBytes(result.download_bytes)}</td>
          <td>${result.success ? "成功" : escapeHTML(result.error || "失败")}</td>
        </tr>
      `)
      .join("") || `<tr><td colspan="7">暂无临时测速结果</td></tr>`;
}

function recordItem(record) {
  return `
    <article class="item">
      <div class="item-head">
        <div class="item-title">
          <strong>${escapeHTML(record.fqdn || record.name || "-")}</strong>
          <div class="mono">${escapeHTML(record.value || "-")}</div>
        </div>
        <span class="badge">${escapeHTML(record.nodeid || "-")}</span>
      </div>
      <small>${formatLatency(record.latency_ms)} · ${formatSpeed(record.speed_bps)} · ${formatDate(record.updated_at)}</small>
    </article>
  `;
}

function openSubscriptionDialog(sub = null) {
  $("subscriptionId").value = sub?.id || "";
  $("subscriptionEnabled").checked = sub?.enabled ?? true;
  $("subscriptionName").value = sub?.name || "";
  $("subscriptionToken").value = sub?.public_token || "";
  $("subscriptionKey").value = "";
  $("subscriptionNodeids").value = (sub?.nodeids || []).join(",");
  $("subscriptionShares").value = (sub?.shares || []).join("\n");
  $("dialogSecret").textContent = "";
  $("dialogTitle").textContent = sub ? "编辑订阅" : "新增订阅";
  $("deleteSubscriptionButton").hidden = !sub;
  $("rotateKeyButton").hidden = !sub;
  $("subscriptionDialog").showModal();
}

async function saveSubscription() {
  const id = $("subscriptionId").value;
  const payload = subscriptionPayload();
  setBusy(true);
  try {
    const result = id
      ? await api(`/api/v1/admin/subscriptions/${encodeURIComponent(id)}`, {
          method: "PUT",
          body: JSON.stringify(payload),
        })
      : await api("/api/v1/admin/subscriptions", {
          method: "POST",
          body: JSON.stringify(payload),
        });
    if (result.key) {
      $("subscriptionId").value = result.item.id;
      $("subscriptionToken").value = result.item.public_token || "";
      $("deleteSubscriptionButton").hidden = false;
      $("rotateKeyButton").hidden = false;
      $("dialogSecret").textContent = `新 Key：${result.key}`;
    } else {
      $("subscriptionDialog").close();
    }
    await refreshAll();
    notify("已保存");
  } catch (error) {
    $("dialogSecret").textContent = error.message;
  } finally {
    setBusy(false);
  }
}

async function deleteSubscription() {
  const id = $("subscriptionId").value;
  if (!id || !confirm("删除这个订阅？")) {
    return;
  }
  setBusy(true);
  try {
    await api(`/api/v1/admin/subscriptions/${encodeURIComponent(id)}`, { method: "DELETE" });
    $("subscriptionDialog").close();
    await refreshAll();
    notify("已删除");
  } catch (error) {
    $("dialogSecret").textContent = error.message;
  } finally {
    setBusy(false);
  }
}

async function rotateKey() {
  const id = $("subscriptionId").value;
  if (!id) {
    return;
  }
  setBusy(true);
  try {
    const result = await api(`/api/v1/admin/subscriptions/${encodeURIComponent(id)}/rotate-secret`, {
      method: "POST",
      body: JSON.stringify({ target: "key" }),
    });
    $("dialogSecret").textContent = `新 Key：${result.key}`;
    await refreshAll();
  } catch (error) {
    $("dialogSecret").textContent = error.message;
  } finally {
    setBusy(false);
  }
}

async function runTemporarySpeedTest() {
  const url = $("speedTestUrl").value.trim();
  if (!url) {
    $("speedTestStatus").textContent = "请输入测速下载 URL";
    return;
  }
  setBusy(true);
  $("speedTestStatus").textContent = "测速运行中";
  try {
    const result = await api("/api/v1/admin/speed-tests", {
      method: "POST",
      body: JSON.stringify({ url }),
    });
    state.temporarySpeedTest = result;
    renderTemporarySpeed();
    notify("临时测速完成");
    if (confirm("临时测速完成，是否根据测速结果更新订阅？")) {
      await applyTemporarySpeedTest(true);
    }
  } catch (error) {
    $("speedTestStatus").textContent = error.message;
    notify(error.message);
  } finally {
    setBusy(false);
  }
}

async function applyTemporarySpeedTest(skipConfirm = false) {
  const id = state.temporarySpeedTest?.id;
  if (!id) {
    return;
  }
  if (!skipConfirm && !confirm("根据本次测速结果更新优选记录并影响订阅？")) {
    return;
  }
  setBusy(true);
  try {
    await api(`/api/v1/admin/speed-tests/${encodeURIComponent(id)}/apply`, { method: "POST" });
    state.temporarySpeedTest = null;
    $("speedTestStatus").textContent = "";
    notify("已应用测速结果");
    await refreshAll();
  } catch (error) {
    $("speedTestStatus").textContent = error.message;
    notify(error.message);
  } finally {
    setBusy(false);
  }
}

function subscriptionPayload() {
  return {
    name: $("subscriptionName").value.trim(),
    enabled: $("subscriptionEnabled").checked,
    public_token: $("subscriptionToken").value.trim(),
    key: $("subscriptionKey").value.trim(),
    format: "base64",
    nodeids: splitLines($("subscriptionNodeids").value.replaceAll(",", "\n")),
    shares: splitLines($("subscriptionShares").value),
  };
}

function setTab(tab) {
  state.activeTab = tab;
  const titles = { overview: "总览", subscriptions: "订阅", records: "优选 IP", speed: "测速" };
  $("pageTitle").textContent = titles[tab] || "总览";
  document.querySelectorAll(".tab").forEach((button) => button.classList.toggle("active", button.dataset.tab === tab));
  document.querySelectorAll(".panel").forEach((panel) => panel.classList.remove("active-panel"));
  $(`${tab}Panel`).classList.add("active-panel");
}

function splitLines(value) {
  return value
    .split("\n")
    .map((item) => item.trim())
    .filter(Boolean);
}

function setBusy(busy) {
  document.querySelectorAll("button").forEach((button) => {
    button.disabled = busy && !button.dataset.edit && !button.dataset.copy;
  });
}

function notify(message) {
  toast.textContent = message;
  toast.hidden = false;
  setTimeout(() => {
    toast.hidden = true;
  }, 3200);
}

function empty(message) {
  return `<article class="item"><small>${escapeHTML(message)}</small></article>`;
}

function formatDate(value) {
  if (!value) {
    return "-";
  }
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) {
    return "-";
  }
  return date.toLocaleString();
}

function formatLatency(value) {
  return value ? `${value} ms` : "-";
}

function formatBytes(value) {
  if (!value) {
    return "-";
  }
  const units = ["B", "KB", "MB", "GB"];
  let amount = value;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  return `${amount.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function formatSpeed(value) {
  if (!value) {
    return "-";
  }
  return `${formatBytes(value)}/s`;
}

function escapeHTML(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function escapeAttr(value) {
  return escapeHTML(value);
}

boot();
