const $ = (id) => document.getElementById(id);
const toast = $("toast");
const fields = $("settingsFields");
const saveBtn = $("saveBtn");
let toastTimer;

/** @typedef {{ id: string, name: string, address: string, probe: string, latency: number|null, detail: string }} SrcRow */

const tables = {
  priority: { body: null, rows: /** @type {SrcRow[]} */ ([]) },
  all: { body: null, rows: /** @type {SrcRow[]} */ ([]) },
  met: { body: null, rows: /** @type {SrcRow[]} */ ([]) },
  nodes: { body: null, rows: /** @type {SrcRow[]} */ ([]) },
};

let rowSeq = 0;
const nameCache = new Map(); // address -> name from server.met
const filesCache = new Map(); // address -> files count from server.met

function formatFilesCount(n) {
  const v = Number(n) || 0;
  if (v <= 0) return "";
  if (v >= 100000000) return (v / 100000000).toFixed(1).replace(/\.0$/, "") + "亿";
  if (v >= 10000) return (v / 10000).toFixed(v >= 100000 ? 0 : 1).replace(/\.0$/, "") + "万";
  return String(v);
}

function rememberMetEntry(e) {
  if (!e?.address) return;
  if (e.name) nameCache.set(e.address, e.name);
  if (typeof e.files === "number" && e.files > 0) filesCache.set(e.address, e.files);
}

function labelForServer(address, name) {
  return String(name || nameCache.get(address) || "").trim();
}

function filesLabelFor(address) {
  return formatFilesCount(filesCache.get(address)) || "—";
}

function showToast(msg, isError) {
  toast.textContent = msg;
  toast.classList.toggle("error", !!isError);
  toast.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => toast.classList.remove("show"), 1800);
}

function setSaveMsg(text, kind) {
  const el = $("saveMsg");
  el.textContent = text;
  el.className = "meta" + (kind ? " " + kind : "");
}

function escapeHtml(s) {
  return String(s || "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

function newId() {
  rowSeq += 1;
  return "r" + rowSeq;
}

function hostLabel(url) {
  try {
    const u = new URL(url);
    return u.hostname || url;
  } catch {
    return url.split("/")[2] || url;
  }
}

function makeRow(partial) {
  const address = String(partial.address || "").trim();
  let name = String(partial.name || "").trim();
  // Strip legacy "name · count" labels if still present in memory.
  if (name.includes(" · ")) name = name.split(" · ")[0].trim();
  if (!name && address) {
    name = nameCache.get(address) || (address.includes("://") ? hostLabel(address) : "");
  } else if (address && nameCache.has(address)) {
    name = nameCache.get(address) || name;
  }
  return {
    id: partial.id || newId(),
    name,
    address,
    probe: partial.probe || "idle",
    latency: partial.latency ?? null,
    detail: partial.detail || "",
  };
}

function collectAddresses(key) {
  return tables[key].rows.map((r) => r.address.trim()).filter(Boolean);
}

function normalizeAddr(addr) {
  return String(addr || "").trim().toLowerCase();
}

function priorityAddrSet() {
  return new Set(collectAddresses("priority").map(normalizeAddr));
}

/** Drop rows whose address is already in the priority list. */
function withoutPriority(list) {
  const pri = priorityAddrSet();
  return (list || []).filter((item) => {
    const addr = typeof item === "string" ? item : item?.address;
    const n = normalizeAddr(addr);
    return n && !pri.has(n);
  });
}

function pruneBackupServers() {
  syncAllFromDom();
  const next = withoutPriority(tables.all.rows);
  if (next.length !== tables.all.rows.length) {
    setRows("all", next);
    return true;
  }
  return false;
}

function setRows(key, list) {
  const rows = (list || []).map((item) =>
    typeof item === "string" ? makeRow({ address: item }) : makeRow(item)
  );
  tables[key].rows = key === "all" ? withoutPriority(rows) : rows;
  renderTable(key);
}

function renderTable(key) {
  const t = tables[key];
  if (!t.body) return;
  if (t.rows.length === 0) {
    t.body.innerHTML = `<div class="src-empty" role="row"><span>暂无条目</span></div>`;
    return;
  }
  const showFiles = key === "priority" || key === "all";
  t.body.innerHTML = t.rows
    .map((row) => {
      const probeCls =
        row.probe === "ok"
          ? "is-ok"
          : row.probe === "fail"
            ? "is-fail"
            : row.probe === "pending"
              ? "is-pending"
              : "is-idle";
      let probeText = "—";
      if (row.probe === "pending") probeText = "测…";
      else if (row.probe === "ok")
        probeText = row.latency != null ? `通 ${row.latency}ms` : "通";
      else if (row.probe === "fail") probeText = "不通";
      const title = escapeHtml(row.detail || "");
      const files = showFiles ? filesLabelFor(row.address) : "";
      const filesRaw = showFiles ? filesCache.get(row.address) : 0;
      const filesCell = showFiles
        ? `<span class="src-files" title="${filesRaw ? filesRaw + " 个文件" : "暂无统计"}">${escapeHtml(files)}</span>`
        : "";
      return `<div class="src-row" role="row" data-id="${row.id}">
        <input class="src-name" type="text" value="${escapeHtml(row.name)}" placeholder="名称" spellcheck="false" aria-label="名称" readonly />
        ${filesCell}
        <input class="src-addr" type="text" value="${escapeHtml(row.address)}" placeholder="host:port 或 URL" spellcheck="false" autocomplete="off" aria-label="地址" />
        <button type="button" class="src-probe ${probeCls}" data-probe-row="${key}" title="${title}" aria-label="测通">${probeText}</button>
        <button type="button" class="src-del" data-del="${key}" aria-label="删除">×</button>
      </div>`;
    })
    .join("");
}

function syncRowFromDom(key, id) {
  const el = tables[key].body.querySelector(`.src-row[data-id="${id}"]`);
  const row = tables[key].rows.find((r) => r.id === id);
  if (!el || !row) return;
  row.name = el.querySelector(".src-name").value.trim();
  row.address = el.querySelector(".src-addr").value.trim();
}

function syncAllFromDom() {
  for (const key of Object.keys(tables)) {
    for (const row of tables[key].rows) syncRowFromDom(key, row.id);
  }
}

function fillForm(cfg) {
  $("proxy_url").value = cfg.proxy_url || "";
  $("default_wait_sec").value = cfg.default_wait_sec ?? 20;
  $("default_limit").value = cfg.default_limit ?? 0;
  $("max_limit").value = cfg.max_limit ?? 10000;
  $("listen_port").value = cfg.listen_port ?? 4661;
  $("udp_port").value = cfg.udp_port ?? 4662;
  $("refresh_hours").value = cfg.refresh_hours ?? 8;
  $("enable_kad").checked = !!cfg.enable_kad;
  $("enable_upnp").checked = !!cfg.enable_upnp;
  $("max_priority_servers").value = cfg.max_priority_servers ?? 6;
  $("max_total_servers").value = cfg.max_total_servers ?? 12;
  setRows(
    "priority",
    (cfg.servers || []).map((addr) => ({ address: addr, name: nameCache.get(addr) || "" }))
  );
  // Backup list never repeats priority addresses.
  const pri = new Set((cfg.servers || []).map(normalizeAddr));
  setRows(
    "all",
    (cfg.all_servers || [])
      .filter((addr) => addr && !pri.has(normalizeAddr(addr)))
      .map((addr) => ({ address: addr, name: nameCache.get(addr) || "" }))
  );
  setRows(
    "met",
    (cfg.server_met_urls || []).map((url) => ({ address: url, name: hostLabel(url) }))
  );
  setRows(
    "nodes",
    (cfg.nodes_dat_urls || []).map((url) => ({ address: url, name: hostLabel(url) }))
  );
  $("offline_115_enabled").checked = !!(cfg.offline_115_enabled ?? cfg.offline_115?.enabled);
  setCookieValue(cfg.offline_115_cookie || "");
  const cid = cfg.offline_115_wp_path_id || cfg.offline_115?.wp_path_id || "0";
  const pathName =
    cfg.offline_115_wp_path_name ||
    cfg.offline_115?.wp_path_name ||
    (cid === "0" ? "根目录" : pathLabelForCid(cid));
  setSelectedFolder(cid, pathName);
}

async function enrichServerNames() {
  const urls = collectAddresses("met");
  if (urls.length === 0) return;
  try {
    const data = await fetchServerMetRanked(0);
    for (const e of data.entries || []) {
      rememberMetEntry(e);
    }
    for (const key of ["priority", "all"]) {
      let changed = false;
      for (const row of tables[key].rows) {
        const nextName = labelForServer(row.address, nameCache.get(row.address) || row.name);
        if (nextName && nextName !== row.name) {
          row.name = nextName;
          changed = true;
        }
        if (filesCache.has(row.address)) changed = true;
      }
      if (changed) renderTable(key);
    }
  } catch {
    /* ignore enrichment failures */
  }
}

async function loadConfig() {
  setSaveMsg("加载中…");
  fields.disabled = true;
  saveBtn.disabled = true;
  const res = await fetch("/api/config");
  const cfg = await res.json();
  if (!res.ok) throw new Error(cfg.error || res.statusText);
  fillForm(cfg);
  fields.disabled = false;
  saveBtn.disabled = false;
  setSaveMsg("");
  enrichServerNames();
  refreshFolderLabel();
}

async function refreshFolderLabel() {
  const cid = $("offline_115_wp_path_id").value.trim() || "0";
  const cookie = $("offline_115_cookie").value.trim();
  if (!cookie || cid === "0") {
    if (cid === "0") setSelectedFolder("0", "根目录");
    return;
  }
  if (folderPathCache.has(cid)) {
    setSelectedFolder(cid, folderPathCache.get(cid));
    return;
  }
  try {
    const res = await fetch("/api/115/folders", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ cid, cookie }),
    });
    const data = await res.json();
    if (!res.ok) return;
    const path = (data.path || []).map((p) => p.name || p.cid).join(" / ") || pathLabelForCid(cid);
    setSelectedFolder(cid, path);
  } catch {
    /* keep cid fallback label */
  }
}

$("settingsForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  syncAllFromDom();
  saveBtn.disabled = true;
  saveBtn.setAttribute("aria-busy", "true");
  setSaveMsg("保存中…");
  const body = {
    proxy_url: $("proxy_url").value.trim(),
    default_wait_sec: Number($("default_wait_sec").value),
    default_limit: Number($("default_limit").value),
    max_limit: Number($("max_limit").value),
    listen_port: Number($("listen_port").value),
    udp_port: Number($("udp_port").value),
    refresh_hours: Number($("refresh_hours").value),
    enable_kad: $("enable_kad").checked,
    enable_upnp: $("enable_upnp").checked,
    max_priority_servers: Number($("max_priority_servers").value),
    max_total_servers: Number($("max_total_servers").value),
    servers: collectAddresses("priority"),
    all_servers: withoutPriority(collectAddresses("all")),
    server_met_urls: collectAddresses("met"),
    nodes_dat_urls: collectAddresses("nodes"),
    offline_115_enabled: $("offline_115_enabled").checked,
    offline_115_cookie: $("offline_115_cookie").value.trim(),
    offline_115_wp_path_id: $("offline_115_wp_path_id").value.trim() || "0",
    offline_115_wp_path_name: $("offline115PathLabel").textContent.trim() || "根目录",
  };
  try {
    const res = await fetch("/api/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const cfg = await res.json();
    if (!res.ok) throw new Error(cfg.error || res.statusText);
    fillForm(cfg);
    enrichServerNames();
    if (cfg.restart_required) {
      const need = (cfg.need_restart || []).join("、") || "部分项";
      setSaveMsg(`已保存并热更新。仍需重启：${need}`, "ok");
      showToast("已保存，部分需重启");
    } else {
      const applied = (cfg.applied || []).join("、");
      setSaveMsg(applied ? `已保存并立即生效：${applied}` : cfg.note || "已保存。", "ok");
      showToast("已保存并生效");
    }
  } catch (err) {
    setSaveMsg(err.message || String(err), "error");
    showToast("保存失败", true);
  } finally {
    saveBtn.disabled = false;
    saveBtn.removeAttribute("aria-busy");
  }
});

tables.priority.body = $("priorityBody");
tables.all.body = $("allServersBody");
tables.met.body = $("metBody");
tables.nodes.body = $("nodesBody");

document.addEventListener("click", (e) => {
  const add = e.target.closest("[data-add]");
  if (add) {
    const key = add.getAttribute("data-add");
    syncAllFromDom();
    tables[key].rows.push(makeRow({}));
    renderTable(key);
    const last = tables[key].body.querySelector(".src-row:last-child .src-addr");
    if (last) last.focus();
    return;
  }
  const del = e.target.closest("[data-del]");
  if (del) {
    const key = del.getAttribute("data-del");
    const rowEl = del.closest(".src-row");
    if (!rowEl) return;
    syncAllFromDom();
    const id = rowEl.getAttribute("data-id");
    tables[key].rows = tables[key].rows.filter((r) => r.id !== id);
    renderTable(key);
    return;
  }
  const probeRow = e.target.closest("[data-probe-row]");
  if (probeRow) {
    const key = probeRow.getAttribute("data-probe-row");
    const rowEl = probeRow.closest(".src-row");
    if (!rowEl) return;
    syncRowFromDom(key, rowEl.getAttribute("data-id"));
    const row = tables[key].rows.find((r) => r.id === rowEl.getAttribute("data-id"));
    if (row) probeOne(key, row);
    return;
  }
  const probeTable = e.target.closest("[data-probe-table]");
  if (probeTable) {
    const key = probeTable.getAttribute("data-probe-table");
    probeTables([key]);
  }
});

document.addEventListener("change", (e) => {
  const rowEl = e.target.closest(".src-row");
  if (!rowEl) return;
  const body = rowEl.parentElement;
  const key = Object.keys(tables).find((k) => tables[k].body === body);
  if (!key) return;
  syncRowFromDom(key, rowEl.getAttribute("data-id"));
  if (e.target.classList.contains("src-addr")) {
    const row = tables[key].rows.find((r) => r.id === rowEl.getAttribute("data-id"));
    if (row) {
      row.name = labelForServer(
        row.address,
        nameCache.get(row.address) || (row.address.includes("://") ? hostLabel(row.address) : "")
      );
      rowEl.querySelector(".src-name").value = row.name;
    }
    if (key === "priority") pruneBackupServers();
    if (key === "all" && row && priorityAddrSet().has(normalizeAddr(row.address))) {
      showToast("该地址已在优先列表，已从备用中排除", true);
      pruneBackupServers();
    }
  }
});

const kindLabel = {
  server: "服务器",
  server_met: "server.met",
  nodes_dat: "nodes.dat",
};

function applyProbeResults(items) {
  const byTarget = new Map();
  for (const it of items || []) {
    byTarget.set(it.target, it);
  }
  const mapKind = { priority: "server", all: "server", met: "server_met", nodes: "nodes_dat" };
  for (const key of Object.keys(tables)) {
    let changed = false;
    for (const row of tables[key].rows) {
      const it = byTarget.get(row.address);
      if (!it) continue;
      if (mapKind[key] && it.kind !== mapKind[key]) continue;
      row.probe = it.ok ? "ok" : "fail";
      row.latency = it.latency_ms ?? null;
      row.detail = it.ok ? it.detail || "" : it.error || "";
      changed = true;
    }
    if (changed) renderTable(key);
  }
}

function renderProbe(data) {
  const box = $("probeResults");
  const summary = $("probeSummary");
  const dialog = $("probeDialog");
  const items = data.items || [];
  applyProbeResults(items);
  if (items.length === 0) {
    summary.className = "probe-summary is-empty";
    summary.innerHTML = `<p class="probe-summary-title">没有可测试的条目</p>`;
    box.innerHTML = "";
    dialog.showModal();
    return;
  }
  const order = { server: 0, server_met: 1, nodes_dat: 2 };
  items.sort(
    (a, b) =>
      (order[a.kind] ?? 9) - (order[b.kind] ?? 9) ||
      String(a.target).localeCompare(String(b.target))
  );
  const okN = items.filter((x) => x.ok).length;
  const failN = items.length - okN;
  const allOk = failN === 0;
  const byKind = { server: 0, server_met: 0, nodes_dat: 0 };
  const byKindOk = { server: 0, server_met: 0, nodes_dat: 0 };
  items.forEach((it) => {
    if (byKind[it.kind] !== undefined) {
      byKind[it.kind] += 1;
      if (it.ok) byKindOk[it.kind] += 1;
    }
  });
  const rows = items
    .map((it) => {
      const cls = it.ok ? "ok" : "error";
      const status = it.ok ? "通过" : "失败";
      const detail = it.ok ? it.detail || "" : it.error || "";
      return `<div class="probe-row ${cls}">
        <span class="probe-kind">${kindLabel[it.kind] || it.kind}</span>
        <span class="probe-target">${escapeHtml(it.target)}</span>
        <span class="probe-status">${status}<em>${it.latency_ms ?? "-"} ms</em></span>
        <span class="probe-detail">${escapeHtml(detail)}</span>
      </div>`;
    })
    .join("");

  summary.className = "probe-summary " + (allOk ? "is-ok" : "is-partial");
  summary.innerHTML = `
    <div class="probe-score">
      <span class="probe-score-mark" aria-hidden="true">${allOk ? "✓" : "!"}</span>
      <div class="probe-score-text">
        <p class="probe-summary-title">${allOk ? "全部通过" : "部分失败"}</p>
        <p class="probe-summary-ratio"><strong>${okN}</strong> / ${items.length} 项可达</p>
      </div>
      <div class="probe-score-chips" aria-label="分类统计">
        <span class="probe-chip">服务器 ${byKindOk.server}/${byKind.server}</span>
        <span class="probe-chip">server.met ${byKindOk.server_met}/${byKind.server_met}</span>
        <span class="probe-chip">nodes.dat ${byKindOk.nodes_dat}/${byKind.nodes_dat}</span>
      </div>
    </div>
    <p class="probe-note">${escapeHtml(data.note || "仅测试连通与可解析性，不会修改配置。")}</p>`;
  box.innerHTML = rows;
  dialog.showModal();
}

function closeProbeDialog() {
  const dialog = $("probeDialog");
  if (dialog.open) dialog.close();
}

$("probeCloseBtn").addEventListener("click", closeProbeDialog);
$("probeDoneBtn").addEventListener("click", closeProbeDialog);
$("probeDialog").addEventListener("click", (e) => {
  if (e.target === $("probeDialog")) closeProbeDialog();
});

function markPending(keys) {
  for (const key of keys) {
    for (const row of tables[key].rows) {
      if (row.address.trim()) {
        row.probe = "pending";
        row.latency = null;
        row.detail = "";
      }
    }
    renderTable(key);
  }
}

async function runProbe(body, { showDialog } = { showDialog: true }) {
  const res = await fetch("/api/probe", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || res.statusText);
  if (showDialog) renderProbe(data);
  else applyProbeResults(data.items || []);
  return data;
}

async function probeTables(keys, { showDialog } = { showDialog: false }) {
  syncAllFromDom();
  const msg = $("probeMsg");
  msg.className = "meta";
  msg.textContent = "测试中…";
  markPending(keys);

  const servers = [];
  const server_met_urls = [];
  const nodes_dat_urls = [];
  for (const key of keys) {
    const addrs = collectAddresses(key);
    if (key === "priority" || key === "all") servers.push(...addrs);
    if (key === "met") server_met_urls.push(...addrs);
    if (key === "nodes") nodes_dat_urls.push(...addrs);
  }
  // Deduplicate servers while preserving order
  const seen = new Set();
  const uniqServers = servers.filter((a) => {
    if (seen.has(a)) return false;
    seen.add(a);
    return true;
  });

  try {
    const data = await runProbe(
      { servers: uniqServers, server_met_urls, nodes_dat_urls },
      { showDialog }
    );
    const okN = (data.items || []).filter((x) => x.ok).length;
    const total = (data.items || []).length;
    msg.className = okN === total ? "meta ok" : "meta error";
    msg.textContent = `完成：${okN}/${total} 通过`;
    showToast(okN === total ? "连通性测试全部通过" : "部分不可达", okN !== total);
  } catch (err) {
    msg.className = "meta error";
    msg.textContent = err.message || String(err);
    showToast("测试失败", true);
    for (const key of keys) {
      for (const row of tables[key].rows) {
        if (row.probe === "pending") {
          row.probe = "idle";
        }
      }
      renderTable(key);
    }
  }
}

async function probeOne(key, row) {
  if (!row.address.trim()) {
    showToast("请先填写地址", true);
    return;
  }
  row.probe = "pending";
  row.latency = null;
  row.detail = "";
  renderTable(key);
  const body = { servers: [], server_met_urls: [], nodes_dat_urls: [] };
  if (key === "priority" || key === "all") body.servers = [row.address];
  if (key === "met") body.server_met_urls = [row.address];
  if (key === "nodes") body.nodes_dat_urls = [row.address];
  try {
    await runProbe(body, { showDialog: false });
  } catch (err) {
    row.probe = "fail";
    row.detail = err.message || String(err);
    renderTable(key);
    showToast("测试失败", true);
  }
}

$("probeBtn").addEventListener("click", () => {
  probeTables(["priority", "all", "met", "nodes"], { showDialog: true });
});

async function fetchServerMetRanked(limit) {
  syncAllFromDom();
  const res = await fetch("/api/server-met", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      server_met_urls: collectAddresses("met"),
      limit: limit > 0 ? limit : 0,
    }),
  });
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

$("fillPriorityBtn").addEventListener("click", async () => {
  const btn = $("fillPriorityBtn");
  const msg = $("probeMsg");
  btn.disabled = true;
  msg.className = "meta";
  msg.textContent = "按文件数拉取优先列表…";
  const n = Math.max(1, Number($("max_priority_servers").value) || 6);
  try {
    const data = await fetchServerMetRanked(n);
    const entries = data.entries || [];
    if (entries.length === 0) {
      msg.className = "meta error";
      msg.textContent = "server.met 未解析到服务器";
      showToast("未解析到服务器", true);
      return;
    }
    for (const e of entries) {
      rememberMetEntry(e);
    }
    setRows(
      "priority",
      entries.map((e) => ({
        address: e.address,
        name: e.name || "",
        probe: "idle",
      }))
    );
    pruneBackupServers();
    const top = entries[0] || {};
    msg.className = "meta ok";
    msg.textContent = `优先 ${entries.length} 台（文件 Top${n}，未保存）`;
    showToast(
      top.files
        ? `已填充优先 ${entries.length} 台（最高 ${formatFilesCount(top.files)}）`
        : `已填充优先 ${entries.length} 台`
    );
  } catch (err) {
    msg.className = "meta error";
    msg.textContent = err.message || String(err);
    showToast("填充失败", true);
  } finally {
    btn.disabled = false;
  }
});

$("fillAllBtn").addEventListener("click", async () => {
  const btn = $("fillAllBtn");
  const msg = $("probeMsg");
  btn.disabled = true;
  msg.className = "meta";
  msg.textContent = "按文件数拉取完整列表…";
  try {
    const data = await fetchServerMetRanked(0);
    const entries = data.entries || [];
    if (entries.length === 0) {
      msg.className = "meta error";
      msg.textContent = "server.met 未解析到服务器";
      showToast("未解析到服务器", true);
      return;
    }
    for (const e of entries) {
      rememberMetEntry(e);
    }
    setRows(
      "all",
      withoutPriority(
        entries.map((e) => ({
          address: e.address,
          name: e.name || "",
          probe: "idle",
        }))
      )
    );
    msg.className = "meta ok";
    msg.textContent = `完整列表 ${entries.length} 台（按文件数，未保存）`;
    showToast(`已填充完整列表 ${entries.length} 台`);
  } catch (err) {
    msg.className = "meta error";
    msg.textContent = err.message || String(err);
    showToast("填充失败", true);
  } finally {
    btn.disabled = false;
  }
});

/* ---- 115 cookie mask ---- */
let cookieRevealed = false;

function maskCookiePreview(raw) {
  const s = String(raw || "").trim();
  if (!s) return "";
  const uid = (s.match(/UID=([^;]+)/i) || [])[1];
  const head = uid ? `UID=${uid.slice(0, 4)}…` : s.slice(0, 6) + "…";
  return `已填写 · ${head} ·••••••••`;
}

function updateCookieView(forceEdit) {
  const ta = $("offline_115_cookie");
  const edit = $("cookieEdit");
  const mask = $("cookieMask");
  const has = !!ta.value.trim();
  if (!has || forceEdit || cookieRevealed) {
    edit.hidden = false;
    mask.hidden = true;
    $("cookieRevealBtn").textContent = has && cookieRevealed ? "隐藏" : "显示";
    return;
  }
  edit.hidden = true;
  mask.hidden = false;
  $("cookieMaskValue").textContent = maskCookiePreview(ta.value);
  $("cookieMaskValue").title = "Cookie 已隐藏";
  $("cookieRevealBtn").textContent = "显示";
}

function setCookieValue(value) {
  cookieRevealed = false;
  $("offline_115_cookie").value = value || "";
  updateCookieView(false);
}

$("offline_115_cookie").addEventListener("blur", () => {
  if ($("offline_115_cookie").value.trim()) {
    cookieRevealed = false;
    updateCookieView(false);
  }
});

$("cookieRevealBtn").addEventListener("click", () => {
  if (!$("offline_115_cookie").value.trim()) {
    updateCookieView(true);
    $("offline_115_cookie").focus();
    return;
  }
  cookieRevealed = !cookieRevealed;
  updateCookieView(false);
  if (cookieRevealed) $("offline_115_cookie").focus();
});

$("cookieChangeBtn").addEventListener("click", () => {
  cookieRevealed = true;
  $("offline_115_cookie").value = "";
  updateCookieView(true);
  $("offline_115_cookie").focus();
});

/* ---- 115 folder picker ---- */
const folderDialog = $("folderDialog");
const folderCrumb = $("folderCrumb");
const folderList = $("folderList");
const folderMsg = $("folderMsg");
const folderSelectBtn = $("folderSelectBtn");
let folderBrowseCid = "0";
let folderBrowsePath = [{ cid: "0", name: "根目录" }];
const folderPathCache = new Map(); // cid -> display path

function pathLabelForCid(cid) {
  const id = String(cid || "0");
  if (id === "0") return "根目录";
  return folderPathCache.get(id) || `文件夹 cid ${id}`;
}

function setSelectedFolder(cid, label) {
  const id = String(cid || "0").trim() || "0";
  const text = label || pathLabelForCid(id);
  $("offline_115_wp_path_id").value = id;
  const el = $("offline115PathLabel");
  el.textContent = text;
  el.title = text + (id !== "0" ? ` (${id})` : "");
  if (id !== "0") folderPathCache.set(id, text);
}

function setFolderMsg(text, isError) {
  folderMsg.textContent = text || "";
  folderMsg.className = "folder-msg meta" + (isError ? " is-error" : "");
}

function renderFolderCrumb() {
  folderCrumb.innerHTML = folderBrowsePath
    .map((p, i) => {
      const last = i === folderBrowsePath.length - 1;
      const sep = i === 0 ? "" : `<span class="sep">/</span>`;
      if (last) {
        return `${sep}<button type="button" aria-current="page">${escapeHtml(p.name)}</button>`;
      }
      return `${sep}<button type="button" data-cid="${escapeHtml(p.cid)}">${escapeHtml(p.name)}</button>`;
    })
    .join("");
}

function renderFolderList(folders) {
  if (!folders || folders.length === 0) {
    folderList.innerHTML = `<div class="folder-empty">此目录下没有子文件夹，可直接选择当前文件夹</div>`;
    return;
  }
  folderList.innerHTML = folders
    .map(
      (f) =>
        `<button type="button" class="folder-item" role="listitem" data-cid="${escapeHtml(f.cid)}" data-name="${escapeHtml(f.name)}">
          <span class="ico" aria-hidden="true"></span>
          <span class="name">${escapeHtml(f.name)}</span>
        </button>`
    )
    .join("");
}

async function loadFolders(cid) {
  const target = String(cid || "0");
  setFolderMsg("加载中…");
  folderList.innerHTML = "";
  folderSelectBtn.disabled = true;
  const cookie = $("offline_115_cookie").value.trim();
  try {
    const res = await fetch("/api/115/folders", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ cid: target, cookie }),
    });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || res.statusText);
    folderBrowseCid = String(data.cid || target);
    if (Array.isArray(data.path) && data.path.length > 0) {
      folderBrowsePath = data.path.map((p) => ({
        cid: String(p.cid || "0"),
        name: p.name || (String(p.cid) === "0" ? "根目录" : String(p.cid)),
      }));
    } else {
      folderBrowsePath = [{ cid: "0", name: "根目录" }];
    }
    renderFolderCrumb();
    renderFolderList(data.folders || []);
    const label = folderBrowsePath.map((p) => p.name).join(" / ");
    folderPathCache.set(folderBrowseCid, label);
    setFolderMsg(data.count ? `${data.count} 个子文件夹` : "无子文件夹");
    folderSelectBtn.disabled = false;
  } catch (err) {
    setFolderMsg(err.message || String(err), true);
    folderSelectBtn.disabled = true;
  }
}

function openFolderPicker() {
  const cookie = $("offline_115_cookie").value.trim();
  if (!cookie) {
    showToast("请先填写 Cookie", true);
    $("offline_115_cookie").focus();
    return;
  }
  const start = $("offline_115_wp_path_id").value.trim() || "0";
  folderDialog.showModal();
  loadFolders(start);
}

folderCrumb.addEventListener("click", (e) => {
  const btn = e.target.closest("button[data-cid]");
  if (!btn) return;
  loadFolders(btn.getAttribute("data-cid"));
});

folderList.addEventListener("click", (e) => {
  const btn = e.target.closest(".folder-item[data-cid]");
  if (!btn) return;
  loadFolders(btn.getAttribute("data-cid"));
});

folderSelectBtn.addEventListener("click", () => {
  const label = folderBrowsePath.map((p) => p.name).join(" / ");
  setSelectedFolder(folderBrowseCid, label);
  folderDialog.close();
  showToast("已选择文件夹");
});

function closeFolderDialog() {
  if (folderDialog.open) folderDialog.close();
}

$("pick115FolderBtn").addEventListener("click", openFolderPicker);
$("folderCloseBtn").addEventListener("click", closeFolderDialog);
$("folderCancelBtn").addEventListener("click", closeFolderDialog);

loadConfig().catch((err) => {
  fields.disabled = true;
  saveBtn.disabled = true;
  setSaveMsg(err.message || String(err), "error");
});
