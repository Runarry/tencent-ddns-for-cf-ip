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
  subscriptionConflicts: [],
  speedPresets: defaultSpeedPresets,
  temporarySpeedTest: null,
  activeTab: "overview",
  editingSubscription: null,
  editingSources: [],
  revealedURLs: new Map(),
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
    const error = new Error(message || `HTTP ${response.status}`);
    error.status = response.status;
    throw error;
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
  $("restoreSubscriptionButton").addEventListener("click", restoreSubscription);
  $("refreshSourcesButton").addEventListener("click", () => refreshSubscriptionSources($("subscriptionId").value));
  $("previewSubscriptionButton").addEventListener("click", () => previewSubscription($("subscriptionId").value, true));
  $("addShareSourceButton").addEventListener("click", () => addSource("share"));
  $("addRemoteSourceButton").addEventListener("click", () => addSource("remote"));
  $("sourceEditor").addEventListener("input", updateSourceFromInput);
  $("sourceEditor").addEventListener("change", updateSourceFromInput);
  $("sourceEditor").addEventListener("click", handleSourceAction);
  $("subscriptionMode").addEventListener("change", updateSubscriptionModeUI);
  $("subscriptionSearch").addEventListener("input", renderSubscriptions);
  $("subscriptionModeFilter").addEventListener("change", renderSubscriptions);
  $("subscriptionHealthFilter").addEventListener("change", renderSubscriptions);
  $("subscriptionList").addEventListener("click", handleSubscriptionAction);
  $("closePreviewButton").addEventListener("click", () => $("previewDialog").close());
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
    state.subscriptionConflicts = subscriptions.override_conflicts || [];
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
  renderSubscriptionConflicts();
  const search = $("subscriptionSearch").value.trim().toLocaleLowerCase();
  const mode = $("subscriptionModeFilter").value;
  const health = $("subscriptionHealthFilter").value;
  const subscriptions = state.subscriptions.filter((sub) => {
    const haystack = [sub.name, sub.id, ...(sub.nodeids || [])].filter(Boolean).join(" ").toLocaleLowerCase();
    return (!search || haystack.includes(search)) &&
      (mode === "all" || normalizedMode(sub) === mode) &&
      (health === "all" || subscriptionHealth(sub).value === health);
  });

  $("subscriptionResultCount").textContent = `显示 ${subscriptions.length} / ${state.subscriptions.length} 个订阅`;
  $("subscriptionList").innerHTML = subscriptions.map(subscriptionCard).join("") || empty("没有符合筛选条件的订阅");
}

function renderSubscriptionConflicts() {
  const banner = $("subscriptionConflictBanner");
  const conflicts = state.subscriptionConflicts || [];
  banner.hidden = conflicts.length === 0;
  banner.innerHTML = conflicts.length
    ? `<strong>有 ${conflicts.length} 个 YAML 覆盖未应用</strong><ul>${conflicts.map((item) =>
        `<li><code>${escapeHTML(item.id || "未知 ID")}</code>：${escapeHTML(item.reason || "配置冲突")}</li>`
      ).join("")}</ul><span>请为 YAML 订阅设置稳定且唯一的 id，然后重新编辑并保存。</span>`
    : "";
}

function subscriptionCard(sub) {
  const sources = normalizeSources(sub);
  const directCount = firstNumber(sub.direct_source_count, sub.direct_count, sub.share_source_count, sub.source_counts?.share, sources.filter((source) => source.type === "share").length);
  const remoteCount = firstNumber(sub.remote_source_count, sub.remote_count, sub.source_counts?.remote, sources.filter((source) => source.type === "remote").length);
  const resolvedCount = firstNumber(sub.resolved_count, sub.parsed_count, sub.parse_count, sumField(sources, "resolved_count"), sub.share_count);
  const outputCount = firstNumber(sub.output_count, sub.generated_count, sub.share_count);
  const status = subscriptionHealth(sub);
  const refreshedAt = sub.last_refreshed_at || sub.last_refresh_at || sub.refreshed_at || latestDate(sources.map((source) => source.last_success_at));
  const override = overrideLabel(sub);
  const revealedURL = state.revealedURLs.get(String(sub.id));
  return `
    <article class="item subscription-card">
      <div class="item-head">
        <div class="item-title">
          <div class="title-line">
            <strong>${escapeHTML(sub.name || "未命名订阅")}</strong>
            <span class="mode-chip mode-${escapeAttr(normalizedMode(sub))}">${escapeHTML(modeLabel(normalizedMode(sub)))}</span>
          </div>
          <small class="mono">${escapeHTML(sub.id || "-")}</small>
        </div>
        <span class="health-badge health-${escapeAttr(status.value)}"><span aria-hidden="true"></span>${escapeHTML(status.label)}</span>
      </div>
      <div class="subscription-stats">
        <div><span>来源</span><strong>${directCount} 直连 / ${remoteCount} 远程</strong></div>
        <div><span>解析 / 输出</span><strong>${formatCount(resolvedCount)} / ${formatCount(outputCount)}</strong></div>
        <div><span>最近刷新</span><strong>${escapeHTML(formatDate(refreshedAt))}</strong></div>
        <div><span>配置状态</span><strong>${escapeHTML(override)}</strong></div>
      </div>
      ${revealedURL ? `<div class="revealed-url"><span class="mono">${escapeHTML(revealedURL)}</span><button class="text-button" type="button" data-action="copy-url" data-id="${escapeAttr(sub.id)}">复制</button></div>` : ""}
      <div class="card-footer">
        <small>${escapeHTML(subscriptionLineLabel(sub))}</small>
        <div class="actions">
          <button class="secondary" type="button" data-action="reveal-url" data-id="${escapeAttr(sub.id)}">${revealedURL ? "隐藏地址" : "显示完整 URL"}</button>
          ${remoteCount ? `<button class="secondary" type="button" data-action="refresh-sources" data-id="${escapeAttr(sub.id)}">刷新远程源</button>` : ""}
          <button class="secondary" type="button" data-action="preview" data-id="${escapeAttr(sub.id)}">预览</button>
          <button class="primary subtle-primary" type="button" data-action="edit" data-id="${escapeAttr(sub.id)}">编辑</button>
        </div>
      </div>
    </article>`;
}

async function handleSubscriptionAction(event) {
  const button = event.target.closest("button[data-action]");
  if (!button) {
    return;
  }
  const id = button.dataset.id;
  const sub = state.subscriptions.find((item) => String(item.id) === id);
  try {
    if (button.dataset.action === "edit") await openSubscriptionDialog(sub);
    if (button.dataset.action === "preview") await previewSubscription(id);
    if (button.dataset.action === "refresh-sources") await refreshSubscriptionSources(id);
    if (button.dataset.action === "reveal-url") await revealSubscriptionURL(sub);
    if (button.dataset.action === "copy-url") await copyText(state.revealedURLs.get(id));
  } catch (error) {
    notify(error.message);
  }
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

async function openSubscriptionDialog(sub = null) {
  let detail = sub;
  if (sub?.id) {
    try {
      const result = await api(`/api/v1/admin/subscriptions/${encodeURIComponent(sub.id)}`);
      detail = result?.item || result?.subscription || result || sub;
      if (Array.isArray(result?.sources) && !Array.isArray(detail?.sources)) detail = { ...detail, sources: result.sources };
      detail = { ...sub, ...detail };
    } catch {
      // Older servers only expose the list endpoint; its item remains editable.
    }
  }
  state.editingSubscription = detail;
  state.editingSources = normalizeSources(detail);
  $("subscriptionId").value = detail?.id || "";
  $("subscriptionEnabled").checked = detail?.enabled ?? true;
  $("subscriptionName").value = detail?.name || "";
  $("subscriptionToken").value = detail?.public_token || "";
  $("subscriptionKey").value = "";
  $("subscriptionMode").value = normalizedMode(detail || {});
  $("subscriptionNodeids").value = (detail?.nodeids || []).join(",");
  $("dialogSecret").textContent = "";
  $("dialogTitle").textContent = detail ? "编辑订阅" : "新增订阅";
  const fromConfig = isConfigSubscription(detail);
  $("deleteSubscriptionButton").hidden = !detail || fromConfig;
  $("restoreSubscriptionButton").hidden = !detail || !fromConfig || !detail?.overridden;
  $("rotateKeyButton").hidden = !detail;
  $("refreshSourcesButton").hidden = !detail || !state.editingSources.some((source) => source.type === "remote");
  $("configOverrideHint").hidden = !fromConfig;
  $("configOverrideHint").textContent = fromConfig
    ? `${overrideLabel(detail)}。保存会建立运行时覆盖；“恢复 YAML”会移除覆盖并重新使用配置文件内容。`
    : "";
  renderSourceEditor();
  updateSubscriptionModeUI();
  $("subscriptionDialog").showModal();
  $("subscriptionName").focus();
}

function renderSourceEditor() {
  const editor = $("sourceEditor");
  editor.innerHTML = state.editingSources.map((source, index) => {
    const isRemote = source.type === "remote";
    const fieldValue = isRemote ? source.url : source.share;
    const status = sourceStatus(source);
    return `
      <article class="source-card ${source.enabled ? "" : "source-disabled"}">
        <div class="source-card-head">
          <span class="source-index">${index + 1}</span>
          <span class="mode-chip">${isRemote ? "远程订阅" : "直连分享"}</span>
          <label class="check-row source-toggle">
            <input type="checkbox" data-source-index="${index}" data-source-field="enabled" ${source.enabled ? "checked" : ""} />
            <span>启用</span>
          </label>
          <div class="source-actions">
            <button class="icon compact-icon" type="button" data-source-action="up" data-source-index="${index}" aria-label="上移来源 ${index + 1}" ${index === 0 ? "disabled" : ""}>↑</button>
            <button class="icon compact-icon" type="button" data-source-action="down" data-source-index="${index}" aria-label="下移来源 ${index + 1}" ${index === state.editingSources.length - 1 ? "disabled" : ""}>↓</button>
            <button class="icon compact-icon danger" type="button" data-source-action="delete" data-source-index="${index}" aria-label="删除来源 ${index + 1}">×</button>
          </div>
        </div>
        <label>
          <span>来源名称</span>
          <input type="text" data-source-index="${index}" data-source-field="name" value="${escapeAttr(source.name || "")}" placeholder="${isRemote ? "例如：机场订阅" : "例如：主节点"}" />
        </label>
        <label>
          <span>${isRemote ? "订阅 URL" : "分享字符串"}</span>
          ${isRemote
            ? `<input type="url" data-source-index="${index}" data-source-field="url" value="${escapeAttr(fieldValue || "")}" placeholder="https://example.com/subscription" />`
            : `<textarea rows="3" data-source-index="${index}" data-source-field="share" spellcheck="false" placeholder="vmess://、vless://、trojan:// …">${escapeHTML(fieldValue || "")}</textarea>`}
        </label>
        <div class="source-meta">
          <span class="health-badge health-${escapeAttr(status.value)}"><span aria-hidden="true"></span>${escapeHTML(status.label)}</span>
          <span>解析 ${formatCount(source.resolved_count)}</span>
          <span>成功 ${escapeHTML(formatDate(source.last_success_at))}</span>
          ${source.error ? `<span class="source-error" title="${escapeAttr(source.error)}">${escapeHTML(source.error)}</span>` : ""}
        </div>
      </article>`;
  }).join("") || `<div class="source-empty"><strong>尚未添加来源</strong><span>添加直连分享或远程订阅后才能生成内容。</span></div>`;
}

function addSource(type) {
  state.editingSources.push({
    id: newSourceID(),
    name: "",
    type,
    enabled: true,
    share: "",
    url: "",
    status: "pending",
    last_success_at: null,
    resolved_count: 0,
    error: "",
  });
  renderSourceEditor();
  $("refreshSourcesButton").hidden = !$("subscriptionId").value || !state.editingSources.some((source) => source.type === "remote");
  const inputs = $("sourceEditor").querySelectorAll("input[data-source-field='name']");
  inputs[inputs.length - 1]?.focus();
}

function updateSourceFromInput(event) {
  const index = Number(event.target.dataset.sourceIndex);
  const field = event.target.dataset.sourceField;
  if (!Number.isInteger(index) || !field || !state.editingSources[index]) return;
  state.editingSources[index][field] = field === "enabled" ? event.target.checked : event.target.value;
  if (field === "enabled" && event.type === "change") renderSourceEditor();
}

function handleSourceAction(event) {
  const button = event.target.closest("button[data-source-action]");
  if (!button) return;
  const index = Number(button.dataset.sourceIndex);
  if (!state.editingSources[index]) return;
  if (button.dataset.sourceAction === "delete") state.editingSources.splice(index, 1);
  if (button.dataset.sourceAction === "up" && index > 0) {
    [state.editingSources[index - 1], state.editingSources[index]] = [state.editingSources[index], state.editingSources[index - 1]];
  }
  if (button.dataset.sourceAction === "down" && index < state.editingSources.length - 1) {
    [state.editingSources[index + 1], state.editingSources[index]] = [state.editingSources[index], state.editingSources[index + 1]];
  }
  renderSourceEditor();
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
      const savedItem = result.item || result.subscription || {};
      $("subscriptionId").value = savedItem.id || id;
      $("subscriptionToken").value = savedItem.public_token || $("subscriptionToken").value;
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

async function restoreSubscription() {
  const id = $("subscriptionId").value;
  if (!id || !confirm("移除运行时覆盖并恢复为 YAML 中的配置？")) return;
  setBusy(true);
  try {
    await api(`/api/v1/admin/subscriptions/${encodeURIComponent(id)}/restore`, { method: "POST" });
    $("subscriptionDialog").close();
    await refreshAll();
    notify("已恢复 YAML 配置");
  } catch (error) {
    $("dialogSecret").textContent = error.message;
  } finally {
    setBusy(false);
  }
}

async function refreshSubscriptionSources(id) {
  if (!id) {
    $("dialogSecret").textContent = "请先保存订阅，再刷新远程源";
    return;
  }
  setBusy(true);
  try {
    const inEditor = $("subscriptionDialog").open && $("subscriptionId").value === id;
    const result = await api(`/api/v1/admin/subscriptions/${encodeURIComponent(id)}/refresh-sources`, {
      method: "POST",
      ...(inEditor ? { body: JSON.stringify(subscriptionPayload()) } : {}),
    });
    const updated = result?.item || result?.subscription;
    if (inEditor && updated) {
      state.editingSubscription = { ...state.editingSubscription, ...updated };
      state.editingSources = normalizeSources(state.editingSubscription);
      renderSourceEditor();
    }
    await refreshAll();
    notify("远程源已刷新");
  } catch (error) {
    if ($("subscriptionDialog").open) $("dialogSecret").textContent = error.message;
    else notify(error.message);
  } finally {
    setBusy(false);
  }
}

async function previewSubscription(id, fromEditor = false) {
  if (!id) {
    $("dialogSecret").textContent = "请先保存订阅，再生成预览";
    return;
  }
  setBusy(true);
  try {
    const result = await api(`/api/v1/admin/subscriptions/${encodeURIComponent(id)}/preview`, {
      method: "POST",
      ...(fromEditor ? { body: JSON.stringify(subscriptionPayload()) } : {}),
    });
    const content = result?.content ?? result?.preview ?? result?.output ?? result?.body ?? result?.data ?? result;
    $("previewSummary").textContent = previewSummary(result);
    $("previewContent").textContent = typeof content === "string" ? content : JSON.stringify(content, null, 2);
    $("previewDialog").showModal();
    $("previewContent").focus();
  } catch (error) {
    if (fromEditor) $("dialogSecret").textContent = error.message;
    else notify(error.message);
  } finally {
    setBusy(false);
  }
}

async function revealSubscriptionURL(sub) {
  const id = String(sub?.id || "");
  if (!id) return;
  if (state.revealedURLs.has(id)) {
    state.revealedURLs.delete(id);
    renderSubscriptions();
    return;
  }
  let url = "";
  try {
    const result = await api(`/api/v1/admin/subscriptions/${encodeURIComponent(id)}/reveal-url`, { method: "POST" });
    url = result?.url || result?.full_url || result?.url_template || "";
  } catch (error) {
    // Legacy list responses expose only a redacted/template URL; it is still useful as a fallback.
    url = sub?.url || sub?.full_url || sub?.url_template || "";
    if (!url) throw error;
  }
  state.revealedURLs.set(id, url);
  renderSubscriptions();
}

async function copyText(value) {
  if (!value) return;
  await navigator.clipboard.writeText(value);
  notify("已复制订阅地址");
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
  const sources = state.editingSources.map((source) => ({
    id: source.id,
    name: String(source.name || "").trim(),
    type: source.type === "remote" ? "remote" : "share",
    enabled: source.enabled !== false,
    share: source.type === "share" ? String(source.share || "").trim() : "",
    url: source.type === "remote" ? String(source.url || "").trim() : "",
    status: source.status || "",
    last_success_at: source.last_success_at || null,
    resolved_count: Number(source.resolved_count) || 0,
    error: source.error || "",
  }));
  return {
    name: $("subscriptionName").value.trim(),
    enabled: $("subscriptionEnabled").checked,
    public_token: $("subscriptionToken").value.trim(),
    key: $("subscriptionKey").value.trim(),
    format: "base64",
    mode: $("subscriptionMode").value || "rewrite",
    nodeids: splitLines($("subscriptionNodeids").value.replaceAll(",", "\n")),
    sources,
    // Keep legacy servers usable while sources are being rolled out.
    shares: sources.filter((source) => source.type === "share" && source.enabled && source.share).map((source) => source.share),
  };
}

function normalizeSources(sub) {
  if (Array.isArray(sub?.sources) && (sub.sources.length > 0 || !sub?.shares?.length)) {
    return sub.sources.map((source, index) => ({
      id: source.id || `source-${index + 1}`,
      name: source.name || "",
      type: source.type === "remote" ? "remote" : "share",
      enabled: source.enabled !== false,
      share: source.share || "",
      url: source.url || "",
      status: source.status || "",
      last_success_at: source.last_success_at || source.success_at || null,
      resolved_count: source.resolved_count ?? source.count,
      error: source.error || source.last_error || "",
    }));
  }
  return (sub?.shares || []).map((share, index) => ({
    id: `legacy-share-${index + 1}`,
    name: `直连分享 ${index + 1}`,
    type: "share",
    enabled: true,
    share,
    url: "",
    status: "",
    last_success_at: null,
    resolved_count: undefined,
    error: "",
  }));
}

function normalizedMode(sub) {
  return sub?.mode === "merge" ? "merge" : "rewrite";
}

function subscriptionHealth(sub) {
  if (!sub?.enabled) return { value: "disabled", label: "已停用" };
  const raw = String(sub.health || sub.health_status || sub.status || "").toLowerCase();
  if (["healthy", "ok", "success", "ready"].includes(raw)) return { value: "healthy", label: "健康" };
  if (["degraded", "partial", "stale", "warning"].includes(raw)) return { value: "degraded", label: "部分异常" };
  if (["unhealthy", "error", "failed", "failure"].includes(raw)) return { value: "unhealthy", label: "异常" };
  const enabledSources = normalizeSources(sub).filter((source) => source.enabled);
  const failed = enabledSources.filter((source) => source.error || ["error", "failed", "unhealthy"].includes(String(source.status).toLowerCase()));
  if (failed.length && failed.length === enabledSources.length) return { value: "unhealthy", label: "异常" };
  if (failed.length) return { value: "degraded", label: "部分异常" };
  return { value: "healthy", label: "正常" };
}

function sourceStatus(source) {
  if (!source.enabled) return { value: "disabled", label: "已停用" };
  if (source.error) return { value: "unhealthy", label: "异常" };
  const raw = String(source.status || "").toLowerCase();
  if (["healthy", "ok", "success", "ready"].includes(raw)) return { value: "healthy", label: "健康" };
  if (["error", "failed", "unhealthy", "failure"].includes(raw)) return { value: "unhealthy", label: "异常" };
  if (["degraded", "partial", "stale", "warning"].includes(raw)) return { value: "degraded", label: "部分异常" };
  if (source.last_success_at) return { value: "healthy", label: "已成功" };
  return { value: "degraded", label: source.type === "remote" ? "待刷新" : "未检查" };
}

function isConfigSubscription(sub) {
  return Boolean(sub && (sub.source === "config" || sub.source === "yaml" || sub.static === true || String(sub.id || "").startsWith("config:")));
}

function overrideLabel(sub) {
  const raw = sub?.override_status ?? sub?.config_override ?? sub?.override ?? sub?.overridden;
  if (raw === true || ["override", "overridden", "custom"].includes(String(raw).toLowerCase())) return "运行时覆盖";
  if (raw === false || ["yaml", "original", "inherited"].includes(String(raw).toLowerCase())) return "YAML 原始配置";
  if (isConfigSubscription(sub)) return sub?.editable ? "运行时覆盖" : "YAML 原始配置";
  return "运行时配置";
}

function firstNumber(...values) {
  const value = values.find((item) => item !== null && item !== undefined && Number.isFinite(Number(item)));
  return value === undefined ? null : Number(value);
}

function sumField(items, field) {
  const values = items.map((item) => item[field]).filter((value) => value !== null && value !== undefined && Number.isFinite(Number(value)));
  return values.length ? values.reduce((sum, value) => sum + Number(value), 0) : null;
}

function latestDate(values) {
  const dates = values.filter(Boolean).map((value) => new Date(value)).filter((date) => !Number.isNaN(date.valueOf()));
  return dates.length ? new Date(Math.max(...dates.map((date) => date.valueOf()))).toISOString() : null;
}

function formatCount(value) {
  return value === null || value === undefined ? "-" : Number(value).toLocaleString();
}

function previewSummary(result) {
  if (!result || typeof result !== "object") return "预览已生成";
  const resolved = firstNumber(result.resolved_count, result.parsed_count, result.parse_count);
  const output = firstNumber(result.output_count, result.generated_count, result.count);
  const parts = [];
  if (resolved !== null) parts.push(`解析 ${formatCount(resolved)}`);
  if (output !== null) parts.push(`输出 ${formatCount(output)}`);
  if (Array.isArray(result.warnings) && result.warnings.length) parts.push(`${result.warnings.length} 条警告`);
  return parts.join(" · ") || "预览已生成";
}

function newSourceID() {
  if (globalThis.crypto?.randomUUID) return crypto.randomUUID();
  return `source-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function updateSubscriptionModeUI() {
  const mode = $("subscriptionMode").value || "rewrite";
  $("subscriptionNodeidsLabel").hidden = mode === "merge";
  $("subscriptionModeHint").textContent = mode === "merge"
    ? "所有来源按原顺序合并，不依赖优选记录，也不解析协议。"
    : "已支持协议会改写到优选地址；未知或解析失败的协议会原样保留，并在预览中告警。";
}

function modeLabel(mode) {
  return mode === "merge" ? "原地址直返" : "优选改写";
}

function subscriptionLineLabel(sub) {
  if (sub.mode === "merge") {
    return "不使用优选";
  }
  return (sub.nodeids || []).join(", ") || "全部线路";
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
  document.documentElement.setAttribute("aria-busy", String(busy));
  if (busy) {
    document.querySelectorAll("button").forEach((button) => {
      if (button.dataset.busyManaged) return;
      button.dataset.busyManaged = "true";
      button.dataset.busyWasDisabled = String(button.disabled);
      button.disabled = true;
    });
    return;
  }
  document.querySelectorAll("button[data-busy-managed]").forEach((button) => {
    button.disabled = button.dataset.busyWasDisabled === "true";
    delete button.dataset.busyManaged;
    delete button.dataset.busyWasDisabled;
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
