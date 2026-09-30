const $ = (id) => document.getElementById(id);
const tbody = $("tbody");
const btn = $("btn");
const toast = $("toast");
const resultsPanel = $("resultsPanel");
const PAGE_SIZE = 20;

let toastTimer;
let searchDefaults = { wait: 25, limit: 0 };
let lastResults = [];
let currentPage = 1;
let offline115 = { enabled: false, configured: false, wp_path_id: "0", wp_path_name: "根目录" };
let liveActive = false;      // tbody currently holds streaming rows, not a page
let activeSearch = null;     // AbortController of the in-flight search
let filterType = "all";
let filterMinSources = 0;

function fileExt(r) {
  const ext = String(r.extension || "").toLowerCase().replace(/^\./, "");
  if (ext) return ext;
  const name = String(r.filename || "").toLowerCase();
  const i = name.lastIndexOf(".");
  return i >= 0 ? name.slice(i + 1) : "";
}

function matchesFilters(r) {
  if (filterMinSources > 0 && (r.sources || 0) < filterMinSources) return false;
  if (filterType === "all") return true;
  const ext = fileExt(r);
  if (filterType === "video") {
    return /^(mkv|mp4|avi|rmvb|wmv|flv|mov|ts|m2ts|mpg|mpeg|vob)$/.test(ext);
  }
  if (filterType === "archive") {
    return /^(zip|rar|7z|tar|gz|bz2|iso|torrent)$/.test(ext);
  }
  if (filterType === "doc") {
    return /^(pdf|doc|docx|xls|xlsx|ppt|pptx|txt|epub|mobi)$/.test(ext);
  }
  return true;
}

function filteredResults() {
  if (filterType === "all" && filterMinSources <= 0) return lastResults;
  return lastResults.filter(matchesFilters);
}

function pageCount() {
  return Math.max(1, Math.ceil(filteredResults().length / PAGE_SIZE));
}

function showToast(msg, isError) {
  toast.textContent = msg;
  toast.classList.toggle("error", !!isError);
  toast.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => toast.classList.remove("show"), 1800);
}

function formatSize(n) {
  if (!n || n < 0) return "-";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return (i === 0 ? v : v.toFixed(v >= 10 ? 1 : 2)) + " " + units[i];
}

function escapeHtml(s) {
  return String(s)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

function emptyState(title, detail, extraHtml) {
  const extra = extraHtml ? `<div class="empty-actions">${extraHtml}</div>` : "";
  return `<tr><td colspan="5" class="empty-cell"><div class="empty-state"><strong>${escapeHtml(title)}</strong><span>${escapeHtml(detail)}</span>${extra}</div></td></tr>`;
}

function skeletonRows(n = 5) {
  return Array.from({ length: n }, () => `
    <tr class="skeleton-row result-row" aria-hidden="true">
      <td class="name">
        <span class="skel w-name"></span>
        <span class="row-meta"><span class="skel w-sm"></span><span class="skel w-src"></span></span>
      </td>
      <td class="num desk-only"><span class="skel w-sm"></span></td>
      <td class="num desk-only"><span class="skel w-sm"></span></td>
      <td class="source desk-only"><span class="skel w-src"></span></td>
      <td class="actions"><span class="skel w-btn"></span></td>
    </tr>`).join("");
}

function hideToolbar() {
  $("resultsToolbar").hidden = true;
  $("resultsCount").classList.remove("is-live");
}

function setToolbarVisible(show) {
  $("resultsToolbar").hidden = !show;
}

function renderPager(into, page, totalPages) {
  if (totalPages <= 1) {
    into.innerHTML = "";
    return;
  }
  const buttons = [];
  buttons.push(
    `<button type="button" class="pager-btn" data-page="${page - 1}" ${page <= 1 ? "disabled" : ""}>上一页</button>`
  );

  const windowSize = 5;
  let start = Math.max(1, page - Math.floor(windowSize / 2));
  let end = Math.min(totalPages, start + windowSize - 1);
  start = Math.max(1, end - windowSize + 1);

  if (start > 1) {
    buttons.push(`<button type="button" class="pager-btn" data-page="1">1</button>`);
    if (start > 2) buttons.push(`<span class="pager-ellipsis">…</span>`);
  }
  for (let p = start; p <= end; p++) {
    buttons.push(
      `<button type="button" class="pager-btn${p === page ? " is-active" : ""}" data-page="${p}" ${p === page ? "aria-current=\"page\"" : ""}>${p}</button>`
    );
  }
  if (end < totalPages) {
    if (end < totalPages - 1) buttons.push(`<span class="pager-ellipsis">…</span>`);
    buttons.push(
      `<button type="button" class="pager-btn" data-page="${totalPages}">${totalPages}</button>`
    );
  }

  buttons.push(
    `<button type="button" class="pager-btn" data-page="${page + 1}" ${page >= totalPages ? "disabled" : ""}>下一页</button>`
  );
  into.innerHTML = buttons.join("");
}

function updateToolbar() {
  const rows = filteredResults();
  const total = rows.length;
  const all = lastResults.length;
  if (all === 0) {
    hideToolbar();
    return;
  }
  const totalPages = pageCount();
  if (currentPage > totalPages) currentPage = totalPages;
  if (currentPage < 1) currentPage = 1;

  const from = total === 0 ? 0 : (currentPage - 1) * PAGE_SIZE + 1;
  const to = Math.min(currentPage * PAGE_SIZE, total);
  let label =
    totalPages > 1
      ? `显示 ${total} 条 · 第 ${from}–${to} 条 · 第 ${currentPage}/${totalPages} 页`
      : `显示 ${total} 条`;
  if (total !== all) label += `（共 ${all}）`;
  if (offline115.configured) {
    label += ` · 115 → ${folderShortName(offline115)}`;
  }

  $("resultsCount").classList.remove("is-live");
  $("resultsCount").textContent = label;
  renderPager($("pager"), currentPage, totalPages);
  setToolbarVisible(true);
}

const PHASE_LABEL = {
  "server": "服务器",
  "kad": "KAD",
  "server+kad": "服务器 + KAD",
  "settling": "收敛",
  "enrich": "补搜",
};

// showLiveProgress renders the in-flight status in the same toolbar the
// finished result count uses, so nothing jumps when the search completes.
function showLiveProgress(p) {
  const n = p.count ?? lastResults.length;
  if (n === 0) return;
  const el = $("resultsCount");
  el.classList.add("is-live");
  $("pager").innerHTML = "";
  const phase = PHASE_LABEL[p.phase] || "检索";
  const secs = ((p.elapsed_ms || 0) / 1000).toFixed(1);
  const hasNew = Array.isArray(p.delta) && p.delta.length > 0;
  let liveNote;
  if (p.phase === "enrich") {
    liveNote = hasNew ? "继续补搜中" : "补搜中 · 暂无新增";
  } else {
    liveNote = hasNew ? "持续搜索中" : "等待更多 · 暂无新增";
  }
  el.textContent = `${phase} · 已找到 ${n} 条 · ${secs}s · ${liveNote}`;
  setToolbarVisible(true);
}

// appendLiveRows adds only the newly arrived hits, so the table grows without
// re-rendering (and without a flicker) while the search is running.
function appendLiveRows(delta) {
  if (!delta || delta.length === 0) return;
  if (!liveActive) {
    tbody.innerHTML = "";
    liveActive = true;
  }
  const start = lastResults.length;
  const html = [];
  for (let i = 0; i < delta.length; i++) {
    lastResults.push(delta[i]);
    html.push(rowHtml(delta[i], start + i, true));
  }
  tbody.insertAdjacentHTML("beforeend", html.join(""));
}

function folderShortName(st) {
  const full = String(st?.wp_path_name || "").trim();
  if (!full) {
    return st?.wp_path_id === "0" || !st?.wp_path_id ? "根目录" : "文件夹";
  }
  const parts = full.split(/\s*\/\s*/).filter(Boolean);
  return parts[parts.length - 1] || full;
}

function canOffline() {
  return !!(offline115.enabled && offline115.configured);
}

function rowHtml(r, absIndex, live) {
  const src = String(r.source || "server").toLowerCase();
  const srcLabel = src === "both" ? "server+kad" : src;
  const offlineBtn = canOffline()
    ? `<button type="button" class="ghost offline-btn" data-offline="${absIndex}">转存 115</button>`
    : "";
  const name = String(r.filename || "");
  const size = formatSize(r.size);
  const sources = `${r.sources ?? 0}${r.complete_sources ? " / " + r.complete_sources : ""}`;
  return `
    <tr class="result-row${live ? " live-in" : ""}">
      <td class="name">
        <span class="name-text" title="${escapeHtml(name)}">${escapeHtml(name)}</span>
        <span class="row-meta">
          <span class="row-meta-size">${escapeHtml(size)}</span>
          <span class="row-meta-sep" aria-hidden="true">·</span>
          <span class="row-meta-sources">${escapeHtml(sources)} 源</span>
          <span class="src-badge src-${escapeHtml(src)}">${escapeHtml(srcLabel)}</span>
        </span>
      </td>
      <td class="num desk-only">${escapeHtml(size)}</td>
      <td class="num desk-only">${escapeHtml(sources)}</td>
      <td class="source desk-only"><span class="src-badge src-${escapeHtml(src)}">${escapeHtml(srcLabel)}</span></td>
      <td class="actions">
        <button type="button" class="ghost copy-btn" data-i="${absIndex}">复制 ed2k</button>
        ${offlineBtn}
      </td>
    </tr>`;
}

function renderPage() {
  if (lastResults.length === 0) {
    hideToolbar();
    return;
  }
  const rows = filteredResults();
  if (rows.length === 0) {
    tbody.innerHTML = emptyState("没有匹配过滤条件", "可切换「全部类型」或降低源数要求。");
    updateToolbar();
    return;
  }
  const totalPages = pageCount();
  if (currentPage > totalPages) currentPage = totalPages;
  const start = (currentPage - 1) * PAGE_SIZE;
  const slice = rows.slice(start, start + PAGE_SIZE);
  tbody.innerHTML = slice
    .map((r) => {
      const absIndex = lastResults.indexOf(r);
      return rowHtml(r, absIndex >= 0 ? absIndex : 0);
    })
    .join("");
  updateToolbar();
}

function applyResultFilters() {
  filterType = $("filterType")?.value || "all";
  filterMinSources = Number($("filterMinSources")?.value || 0) || 0;
  currentPage = 1;
  if (!liveActive && lastResults.length > 0) renderPage();
}

$("filterType").addEventListener("change", applyResultFilters);
$("filterMinSources").addEventListener("change", applyResultFilters);

// One delegated listener survives every re-render, so streaming updates do not
// have to re-bind buttons on each appended row.
tbody.addEventListener("click", (e) => {
  const copyEl = e.target.closest("button[data-i]");
  if (copyEl) {
    const item = lastResults[Number(copyEl.dataset.i)];
    if (item?.ed2k) copyText(item.ed2k, copyEl);
    else showToast("无有效 ed2k 链接", true);
    return;
  }
  const offEl = e.target.closest("button[data-offline]");
  if (offEl) {
    const item = lastResults[Number(offEl.dataset.offline)];
    if (item?.ed2k) pushOffline([item.ed2k], offEl);
    else showToast("无有效 ed2k 链接", true);
  }
});

function goToPage(page) {
  const totalPages = pageCount();
  const next = Math.min(totalPages, Math.max(1, page));
  if (next === currentPage) return;
  currentPage = next;
  renderPage();
  resultsPanel.scrollIntoView({ behavior: "smooth", block: "start" });
}

function onPagerClick(e) {
  const btnEl = e.target.closest("[data-page]");
  if (!btnEl || btnEl.disabled) return;
  goToPage(Number(btnEl.dataset.page));
}

$("pager").addEventListener("click", onPagerClick);

async function loadOffline115() {
  try {
    const res = await fetch("/api/115/status");
    if (!res.ok) return;
    offline115 = await res.json();
  } catch {
    offline115 = { enabled: false, configured: false, wp_path_id: "0", wp_path_name: "根目录" };
  }
}

async function pushOffline(urls, button) {
  if (!canOffline()) {
    showOfflineResult(false, "请先在设置中配置 115 网盘");
    return;
  }
  const prev = button ? button.textContent : "";
  if (button) {
    button.disabled = true;
    button.textContent = "转存中…";
  }
  try {
    const res = await fetch("/api/115/offline", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ urls }),
    });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || res.statusText);
    const okN = data.ok_count ?? 0;
    const total = data.count ?? urls.length;
    if (okN > 0) {
      const msg =
        okN === total
          ? "已加入 115 离线任务。"
          : `部分成功：${okN}/${total} 已加入离线任务。`;
      showOfflineResult(true, msg);
      if (button) {
        button.classList.add("copied");
        button.textContent = "已转存";
      }
    } else {
      const msg = data.results?.[0]?.message || "转存失败，请检查 Cookie 或配额。";
      showOfflineResult(false, msg);
      if (button) button.textContent = prev;
    }
  } catch (err) {
    showOfflineResult(false, err.message || String(err));
    if (button) button.textContent = prev;
  } finally {
    if (button) {
      button.disabled = false;
      if (button.textContent === "转存中…") button.textContent = prev;
      setTimeout(() => {
        if (button.classList.contains("copied")) {
          button.classList.remove("copied");
          button.textContent = prev || "转存 115";
        }
      }, 1400);
    }
  }
}

function showOfflineResult(ok, message) {
  const dialog = $("offlineDialog");
  const title = $("offlineDialogTitle");
  const msg = $("offlineDialogMsg");
  dialog.classList.toggle("is-ok", !!ok);
  dialog.classList.toggle("is-error", !ok);
  title.textContent = ok ? "转存成功" : "转存失败";
  msg.textContent = message || (ok ? "已加入 115 离线任务。" : "请稍后重试。");
  if (!dialog.open) dialog.showModal();
}

$("offlineDialogOk").addEventListener("click", () => {
  const dialog = $("offlineDialog");
  if (dialog.open) dialog.close();
});

$("offlineDialog").addEventListener("click", (e) => {
  if (e.target === $("offlineDialog")) $("offlineDialog").close();
});

async function loadDefaults() {
  try {
    const res = await fetch("/api/config");
    if (!res.ok) return;
    const cfg = await res.json();
    if (cfg.default_wait_sec > 0) {
      searchDefaults.wait = Math.min(cfg.default_wait_sec, 30);
    }
    if (typeof cfg.default_limit === "number") searchDefaults.limit = cfg.default_limit;
    if (cfg.offline_115) offline115 = cfg.offline_115;
  } catch {
    /* keep defaults */
  }
  await loadOffline115();
  renderInitialEmpty();
}

function renderInitialEmpty() {
  const tip = "输入关键词后，会在已连接的 ED2K 服务器与 KAD 中查询。";
  const extra = canOffline()
    ? ""
    : `<a class="ghost empty-link" href="/settings">配置 115 一键离线转存</a>`;
  tbody.innerHTML = emptyState("还没有结果", tip, extra);
}

async function refreshStatus() {
  try {
    const res = await fetch("/api/status");
    const data = await res.json();
    const ok = !!data.ready;
    const searching = !!data.searching;
    $("readyDot").classList.toggle("ok", ok && !searching);
    $("readyDot").classList.toggle("searching", searching);
    $("readyText").textContent = ok ? (searching ? "搜索中" : "就绪") : "未就绪";
    $("brandSignal").classList.toggle("live", ok);
    $("dhtNodes").textContent = data.dht_enabled ? (data.dht_nodes ?? 0) : "关闭";
    $("serverCount").textContent = data.connected_count ?? "-";
  } catch {
    $("readyDot").classList.remove("ok", "searching");
    $("readyText").textContent = "无法连接";
    $("brandSignal").classList.remove("live");
  }
}

async function copyText(text, button) {
  try {
    await navigator.clipboard.writeText(text);
    showToast("ed2k 已复制");
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand("copy");
    document.body.removeChild(ta);
    showToast("ed2k 已复制");
  }
  if (button) {
    const prev = button.textContent;
    button.classList.add("copied");
    button.textContent = "已复制";
    setTimeout(() => {
      button.classList.remove("copied");
      button.textContent = prev;
    }, 1200);
  }
}

function renderResults(data) {
  liveActive = false;
  lastResults = Array.isArray(data.results) ? data.results : [];
  currentPage = 1;
  if (lastResults.length === 0) {
    hideToolbar();
    const running = String(data.state || "").toUpperCase() === "RUNNING";
    const detail = running
      ? "搜索超时结束时仍在进行，暂未收到结果。可再搜一次，或到设置里加大「默认等待」。"
      : `状态：${data.state || "-"}。可换关键词，或到设置里检查服务器连接。`;
    const extra = canOffline()
      ? ""
      : `<a class="ghost empty-link" href="/settings">配置 115 一键离线转存</a>`;
    tbody.innerHTML = emptyState("没有匹配结果", detail, extra);
    return;
  }
  renderPage();
}

function setSearchBusy(busy) {
  btn.disabled = busy;
  if (busy) btn.setAttribute("aria-busy", "true");
  else btn.removeAttribute("aria-busy");
  btn.textContent = busy ? "搜索中…" : "搜索";
  resultsPanel.setAttribute("aria-busy", busy ? "true" : "false");
}

function parseSSEFrame(raw) {
  let event = "message";
  const dataLines = [];
  for (const line of raw.split(/\r?\n/)) {
    if (line === "" || line.startsWith(":")) continue; // blank line or comment
    if (line.startsWith("event:")) event = line.slice(6).trim();
    else if (line.startsWith("data:")) dataLines.push(line.slice(5).replace(/^ /, ""));
  }
  if (dataLines.length === 0) return null;
  const text = dataLines.join("\n");
  let data = text;
  try {
    data = JSON.parse(text);
  } catch {
    /* leave as raw text */
  }
  return { event, data };
}

// Frames are separated by a blank line. Matching the separator directly (rather
// than normalising line endings chunk by chunk) keeps a "\r\n\r\n" that TCP
// split across two reads from being missed.
const SSE_FRAME_SEP = /\r?\n\r?\n/;

// streamSearch drives /api/search/stream. It reads the response body as it
// arrives, so hits show up while the search is still querying servers/KAD.
async function streamSearch(query, opts) {
  const { wait, limit, signal, onProgress, onDone, onServerError } = opts;
  const qs = `q=${encodeURIComponent(query)}&wait=${wait}&limit=${limit}`;
  const res = await fetch(`/api/search/stream?${qs}`, {
    headers: { Accept: "text/event-stream" },
    signal,
  });
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const d = await res.json();
      if (d.error) msg = d.error;
    } catch {
      /* non-JSON error body */
    }
    throw new Error(msg);
  }
  if (!res.body || typeof res.body.getReader !== "function") {
    return streamSearchFallback(query, opts);
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  let finished = false;

  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let m;
    while ((m = SSE_FRAME_SEP.exec(buf)) !== null) {
      const frame = parseSSEFrame(buf.slice(0, m.index));
      buf = buf.slice(m.index + m[0].length);
      if (!frame) continue;
      if (frame.event === "results") onProgress?.(frame.data);
      else if (frame.event === "done") {
        finished = true;
        onDone?.(frame.data);
      } else if (frame.event === "error") {
        finished = true;
        onServerError?.(frame.data);
      }
    }
  }
  return finished;
}

// streamSearchFallback keeps old browsers (and any client without streaming
// fetch) working against the original buffered endpoint.
async function streamSearchFallback(query, opts) {
  const { wait, limit, signal, onDone, onServerError } = opts;
  const qs = `q=${encodeURIComponent(query)}&wait=${wait}&limit=${limit}`;
  const res = await fetch(`/api/search?${qs}`, { signal });
  let data = {};
  try {
    data = await res.json();
  } catch {
    /* ignore */
  }
  if (!res.ok) {
    onServerError?.({ error: data.error || res.statusText, status: res.status });
    return true;
  }
  onDone?.(data);
  return true;
}

$("form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const q = $("q").value.trim();
  if (!q) return;

  // A new query always supersedes the previous one.
  if (activeSearch) activeSearch.abort();
  const ctrl = new AbortController();
  activeSearch = ctrl;

  setSearchBusy(true);
  hideToolbar();
  lastResults = [];
  currentPage = 1;
  liveActive = false;
  tbody.innerHTML = skeletonRows(5);

  const wait = Math.min(searchDefaults.wait || 25, 30);
  const limit = searchDefaults.limit > 0 ? searchDefaults.limit : 0;

  try {
    const finished = await streamSearch(q, {
      wait,
      limit,
      signal: ctrl.signal,
      onProgress: (p) => {
        appendLiveRows(p.delta);
        showLiveProgress(p);
      },
      onDone: (resp) => {
        renderResults(resp);
        if (resp.count > 0) {
          showToast(`搜索完成 · ${resp.count} 条 · ${resp.elapsed_ms} ms`);
        } else {
          showToast("没有匹配结果", true);
        }
      },
      onServerError: (d) => {
        hideToolbar();
        tbody.innerHTML = emptyState("搜索失败", d?.error || "未知错误");
        showToast(d?.status === 409 ? "已有搜索正在进行" : "搜索失败", true);
      },
    });
    if (finished === false && liveActive) {
      // The stream closed without a terminal event; keep what we already have.
      renderResults({ results: lastResults, state: "STOPPED" });
    }
  } catch (err) {
    if (err?.name === "AbortError") return;
    hideToolbar();
    tbody.innerHTML = emptyState("搜索失败", err.message || String(err));
    showToast("搜索失败", true);
  } finally {
    if (activeSearch === ctrl) {
      activeSearch = null;
      setSearchBusy(false);
      refreshStatus();
    }
  }
});

loadDefaults();
refreshStatus();
setInterval(refreshStatus, 5000);
