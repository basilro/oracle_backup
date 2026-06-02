const BUILD = "ui-2026-06-02a";
let csrf = "";
const $ = s => document.querySelector(s);
const esc = s => String(s ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

const FIELDS = [
  { k: "keep_daily",        label: "보존 일수",       type: "num",    unit: "일",   hint: "최근 N일치 스냅샷 유지" },
  { k: "upload_limit_kbps", label: "업로드 제한",      type: "num",    unit: "KB/s", hint: "0 = 무제한" },
  { k: "min_free_mb",       label: "최소 여유 공간",   type: "num",    unit: "MB",   hint: "덤프 전 필요한 여유" },
  { k: "backup_schedule",   label: "백업 스케줄",      type: "cron",   hint: "분 시 일 월 요일 · KST" },
  { k: "check_schedule",    label: "무결성 검증",      type: "cron",   hint: "주간 restic check" },
  { k: "scheduler_enabled", label: "자동 스케줄러",    type: "toggle", hint: "켜면 위 스케줄로 자동 실행" },
  { k: "db_backup_enabled", label: "DB 일관성 백업",   type: "toggle", hint: "덤프 후 백업" },
];

function getCsrf() { const m = document.cookie.match(/csrf=([^;]+)/); if (m) csrf = m[1]; }

/* ---------- tabs (event delegation, robust) ---------- */
function showTab(name) {
  const panels = [...document.querySelectorAll(".tabpanel")];
  if (!panels.some(p => p.dataset.panel === name)) name = "overview";
  document.querySelectorAll("#tabs .tab").forEach(b => b.classList.toggle("active", b.dataset.tab === name));
  panels.forEach(p => { p.hidden = p.dataset.panel !== name; });
  try { history.replaceState(null, "", "#" + name); } catch (e) {}
}
function initTabs() {
  const bar = $("#tabs");
  if (bar) bar.addEventListener("click", e => {
    const b = e.target.closest(".tab");
    if (b && b.dataset.tab) showTab(b.dataset.tab);
  });
  showTab((location.hash || "#overview").slice(1));
}

/* ---------- add remote (Korean form) ---------- */
const ADD_BACKENDS = {
  webdav: [
    { k: "url", label: "URL", required: true, ph: "https://nas.example.com:5006" },
    { k: "vendor", label: "벤더", value: "other", ph: "other / nextcloud / owncloud" },
    { k: "user", label: "아이디" },
    { k: "pass", label: "비밀번호", type: "password" },
  ],
  sftp: [
    { k: "host", label: "호스트", required: true, ph: "nas.example.com" },
    { k: "user", label: "아이디" },
    { k: "pass", label: "비밀번호", type: "password" },
    { k: "port", label: "포트", value: "22" },
  ],
  ftp: [
    { k: "host", label: "호스트", required: true, ph: "ftp.example.com" },
    { k: "user", label: "아이디" },
    { k: "pass", label: "비밀번호", type: "password" },
    { k: "port", label: "포트", value: "21" },
  ],
  s3: [
    { k: "provider", label: "제공자", value: "Other", ph: "AWS / Minio / Other" },
    { k: "access_key_id", label: "Access Key" },
    { k: "secret_access_key", label: "Secret Key", type: "password" },
    { k: "endpoint", label: "엔드포인트", ph: "https://…" },
    { k: "region", label: "리전", ph: "us-east-1" },
  ],
};
function renderAddFields() {
  const fs = ADD_BACKENDS[$("#addType").value] || [];
  $("#addFields").innerHTML = fs.map(f =>
    `<div class="field"><div class="lab">${esc(f.label)}${f.required ? ' <span style="color:var(--fail)">*</span>' : ""}</div>` +
    `<div class="ctl"><input data-pk="${esc(f.k)}" type="${f.type || "text"}" value="${esc(f.value || "")}" placeholder="${esc(f.ph || "")}" style="width:min(260px,60vw)"></div></div>`
  ).join("");
}
async function addRemote() {
  const m = $("#addMsg");
  const name = $("#addName").value.trim();
  const type = $("#addType").value;
  if (!/^[A-Za-z0-9_.-]+$/.test(name)) { m.textContent = "이름은 영문/숫자/_-. 만 가능"; m.className = "msg fail"; return; }
  const params = {};
  document.querySelectorAll("#addFields input").forEach(i => { if (i.value !== "") params[i.dataset.pk] = i.value; });
  for (const f of (ADD_BACKENDS[type] || [])) {
    if (f.required && !params[f.k]) { m.textContent = f.label + " 은(는) 필수"; m.className = "msg fail"; return; }
  }
  const btn = $("#addBtn"); btn.disabled = true; m.textContent = "추가 중…"; m.className = "msg";
  try {
    const r = await api("/api/rclone-add", { method: "POST", body: JSON.stringify({ Name: name, Type: type, Params: params }) });
    if (r.ok) { m.textContent = "✓ 추가됨: " + name; m.className = "msg ok"; $("#addName").value = ""; loadRemotes(); }
    else { m.textContent = "✕ " + (await r.text()); m.className = "msg fail"; }
  } catch (e) { if (String(e.message) !== "unauthorized") { m.textContent = "✕ " + e.message; m.className = "msg fail"; } }
  btn.disabled = false;
}

/* ---------- configured remotes list ---------- */
async function loadRemotes() {
  const t = $("#remoteList"); if (!t) return;
  try {
    const rs = await (await api("/api/rclone-remotes")).json();
    const rows = Array.isArray(rs) && rs.length
      ? rs.map(x => `<tr><td class="mono">${esc(x.name)}</td><td>${esc(x.type)}</td><td>${x.active ? '<span class="st ok">활성</span>' : ""}</td></tr>`).join("")
      : `<tr><td colspan="3" class="empty">설정된 목적지가 없습니다 — 위에서 추가하세요</td></tr>`;
    t.innerHTML = "<thead><tr><th>이름</th><th>유형</th><th></th></tr></thead><tbody>" + rows + "</tbody>";
  } catch (e) {}
}
function toLogin() { location.href = "/login"; }
async function api(p, o = {}) {
  o.headers = Object.assign({ "Content-Type": "application/json", "X-CSRF-Token": csrf }, o.headers || {});
  const r = await fetch(p, o);
  if (r.status === 401) { toLogin(); throw new Error("unauthorized"); }
  return r;
}

/* ---------- helpers ---------- */
function fmtRel(iso) {
  if (!iso) return null;
  const t = Date.parse(iso); if (isNaN(t)) return null;
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return "방금 전";
  if (s < 3600) return Math.floor(s / 60) + "분 전";
  if (s < 86400) return Math.floor(s / 3600) + "시간 전";
  return Math.floor(s / 86400) + "일 전";
}
function fmtB(n) {
  if (!n) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"]; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return n.toFixed(1) + " " + u[i];
}
const DOW = ["일", "월", "화", "수", "목", "금", "토"];
function describeCron(expr) {
  const p = (expr || "").trim().split(/\s+/);
  if (p.length !== 5) return "";
  const [mi, h, dom, mon, dow] = p;
  const num = x => /^\d+$/.test(x);
  if (dom === "*" && mon === "*" && dow === "*" && num(mi) && num(h)) return `매일 ${h.padStart(2,"0")}:${mi.padStart(2,"0")}`;
  if (dom === "*" && mon === "*" && num(dow) && num(mi) && num(h)) return `매주 ${DOW[+dow % 7]} ${h.padStart(2,"0")}:${mi.padStart(2,"0")}`;
  return "";
}

/* ---------- status strip ---------- */
let snapCount = "—";
async function loadStatus() {
  const s = await (await api("/api/status")).json();
  window._busyAtLoad = s.busy;
  $("#host").innerHTML = `<span class="dot">●</span> ${esc(s.host || "host")}`;
  const rel = fmtRel(s.last_success);
  const stateCls = s.busy ? "run" : "idle";
  const stateTxt = s.busy ? "RUNNING" : "IDLE";
  const sched = s.scheduler_enabled
    ? `<span class="pill on">● ON</span>`
    : `<span class="pill off">○ OFF</span>`;
  $("#strip").innerHTML = `
    <div class="cell"><div class="k">현재 상태</div><div class="v"><span class="state ${stateCls}"><span class="led"></span>${stateTxt}</span></div></div>
    <div class="cell"><div class="k">마지막 성공</div><div class="v">${rel ? esc(rel) : "—"}<small>${esc((s.last_success||"").replace("T"," ").replace("Z"," UTC")) || "기록 없음"}</small></div></div>
    <div class="cell"><div class="k">다음 예정</div><div class="v" style="font-size:.98rem">${esc(s.next_run || "—")}</div></div>
    <div class="cell"><div class="k">스케줄러</div><div class="v">${sched}</div></div>
    <div class="cell"><div class="k">스냅샷</div><div class="v">${snapCount}<small>저장된 복원 지점</small></div></div>
    ${s.last_failure ? `<div class="cell"><div class="k" style="color:var(--fail)">마지막 실패</div><div class="v" style="font-size:.86rem;color:var(--fail)">${esc(s.last_failure)}</div></div>` : ""}`;
}

/* ---------- settings ---------- */
async function loadConfig() {
  const c = await (await api("/api/config")).json();
  window._cfg = c;
  $("#config").innerHTML = FIELDS.map(f => {
    let ctl;
    if (f.type === "toggle") {
      ctl = `<label class="toggle"><input type="checkbox" data-k="${f.k}" ${c[f.k] ? "checked" : ""}><span class="track"><span class="knob"></span></span></label>`;
    } else if (f.type === "cron") {
      ctl = `<input class="cron" data-k="${f.k}" value="${esc(c[f.k])}">`;
    } else {
      ctl = `<span class="numwrap"><input type="number" data-k="${f.k}" value="${esc(c[f.k])}"><span class="unit">${f.unit}</span></span>`;
    }
    const hint = f.type === "cron" ? `${f.hint}${describeCron(c[f.k]) ? " · " + describeCron(c[f.k]) : ""}` : f.hint;
    return `<div class="field"><div class="lab">${f.label}<small>${esc(hint)}</small></div><div class="ctl">${ctl}</div></div>`;
  }).join("");
  // live cron hint update
  document.querySelectorAll('.cron').forEach(inp => inp.addEventListener("input", () => {
    const f = FIELDS.find(x => x.k === inp.dataset.k);
    const d = describeCron(inp.value);
    inp.closest(".field").querySelector(".lab small").textContent = f.hint + (d ? " · " + d : "");
  }));
}

async function saveCfg() {
  const get = k => $(`[data-k="${k}"]`);
  const body = {
    KeepDaily: parseInt(get("keep_daily").value),
    UploadLimit: parseInt(get("upload_limit_kbps").value),
    MinFreeMB: parseInt(get("min_free_mb").value),
    BackupSchedule: get("backup_schedule").value,
    CheckSchedule: get("check_schedule").value,
    SchedulerEnabled: get("scheduler_enabled").checked,
    DBBackupEnabled: get("db_backup_enabled").checked,
  };
  const m = $("#cfgMsg");
  const r = await api("/api/config", { method: "PUT", body: JSON.stringify(body) });
  if (r.ok) { m.textContent = "✓ 저장됨"; m.className = "msg ok"; loadStatus(); }
  else { m.textContent = "✕ " + (await r.text()); m.className = "msg fail"; }
}

/* ---------- snapshots / history ---------- */
function fillSnapSelect(snaps) {
  const sel = $("#rsnap"); if (!sel) return;
  if (Array.isArray(snaps) && snaps.length) {
    sel.innerHTML = snaps.slice().reverse().map(x => {
      const id = x.short_id || (x.id || "").slice(0, 8);
      return `<option value="${esc(id)}">${esc((x.time || "").slice(0, 16).replace("T", " "))} · ${esc(id)}</option>`;
    }).join("");
    sel.disabled = false;
  } else {
    sel.innerHTML = `<option value="">스냅샷 없음</option>`; sel.disabled = true;
  }
}

async function loadSnaps(fresh) {
  const s = await (await api("/api/snapshots" + (fresh ? "?fresh=1" : ""))).json();
  snapCount = Array.isArray(s) ? s.length : "—";
  fillSnapSelect(s);
  loadStatus();  // refresh strip's snapshot count once known
  const rows = Array.isArray(s) && s.length
    ? s.slice().reverse().map(x => `<tr>
        <td>${esc((x.time || "").slice(0, 16).replace("T", " "))}</td>
        <td class="mono">${esc(x.short_id || (x.id || "").slice(0, 8))}</td>
        <td>${(x.tags || []).map(t => `<span class="tag">${esc(t)}</span>`).join("")}</td>
        <td class="paths">${(x.paths || []).map(esc).join("<br>")}</td></tr>`).join("")
    : `<tr><td colspan="4" class="empty">스냅샷이 없습니다</td></tr>`;
  $("#snaps").innerHTML = "<thead><tr><th>시각</th><th>ID</th><th>태그</th><th>경로</th></tr></thead><tbody>" + rows + "</tbody>";
}

function stClass(s) { return s === "ok" ? "ok" : (s === "running" ? "run" : "bad"); }
async function loadHistory() {
  const h = await (await api("/api/history")).json();
  const rows = Array.isArray(h) && h.length
    ? h.map(r => `<tr>
        <td>${esc((r.StartedAt || "").replace("T", " ").replace("Z", ""))}</td>
        <td><span class="tag">${esc(r.Trigger)}</span></td>
        <td><span class="st ${stClass(r.Status)}">${esc(r.Status)}</span></td>
        <td>${fmtB(r.DataAdded)}</td></tr>`).join("")
    : `<tr><td colspan="4" class="empty">실행 이력이 없습니다</td></tr>`;
  $("#history").innerHTML = "<thead><tr><th>시작 (UTC)</th><th>트리거</th><th>상태</th><th>추가량</th></tr></thead><tbody>" + rows + "</tbody>";
}

/* ---------- actions ---------- */
let pollTimer = null;
async function backup() {
  const m = $("#actMsg");
  const r = await api("/api/backup", { method: "POST" });
  if (r.status === 202) { m.textContent = "● 백업 시작됨"; m.className = "msg ok"; pollUntilIdle(); }
  else if (r.status === 409) { m.textContent = "이미 진행 중"; m.className = "msg fail"; }
  else { m.textContent = "실패 (" + r.status + ")"; m.className = "msg fail"; }
}
function pollUntilIdle() {
  clearInterval(pollTimer);
  pollTimer = setInterval(async () => {
    try {
      const s = await (await api("/api/status")).json();
      await loadStatus();
      if (!s.busy) { clearInterval(pollTimer); loadHistory(); loadSnaps(true); $("#actMsg").textContent = "✓ 완료"; }
    } catch (e) { clearInterval(pollTimer); }
  }, 4000);
}

/* ---------- rclone GUI (in-dashboard modal via reverse proxy) ---------- */
const sleep = ms => new Promise(r => setTimeout(r, ms));
function rgShowModal(show) {
  $("#rgModal").hidden = !show;
  if (!show) { $("#rgFrame").src = "about:blank"; $("#rgFrame").hidden = true; $("#rgLoading").style.display = ""; }
}
// poll the proxied GUI until it answers, then load it in the iframe
async function rgWaitAndLoad() {
  $("#rgLoading").textContent = "설정 화면 기동 중… (처음 실행은 자산 다운로드로 수십 초 걸릴 수 있습니다)";
  for (let i = 0; i < 40; i++) {
    try { const r = await fetch("/rclone-gui/", { method: "GET" }); if (r.ok || r.status === 401) break; } catch (e) {}
    await sleep(1500);
  }
  $("#rgFrame").hidden = false;
  $("#rgFrame").src = "/rclone-gui/?v=" + Date.now();
  $("#rgFrame").onload = () => { $("#rgLoading").style.display = "none"; autoLoginRclone(); };
}
// The rclone Web GUI always shows a "connect" form (URL pre-filled). With --rc-no-auth,
// clicking Login (blank creds) connects. Auto-click it (same-origin iframe).
function autoLoginRclone() {
  $("#rgModalNote").textContent = "자동 연결되지 않으면 화면의 Login을 한 번 누르세요.";
  let tries = 0;
  const t = setInterval(() => {
    tries++;
    try {
      const fr = $("#rgFrame"), doc = fr.contentDocument, win = fr.contentWindow;
      const onLogin = !win || (win.location.hash || "").includes("/login");
      if (!onLogin) { clearInterval(t); $("#rgModalNote").textContent = ""; return; }
      const btn = [...(doc ? doc.querySelectorAll("button") : [])].find(b => /login|connect/i.test(b.textContent || ""));
      if (btn) btn.click();
    } catch (e) { clearInterval(t); } // cross-origin or gone
    if (tries > 12) clearInterval(t);
  }, 700);
}
async function rgStart() {
  const m = $("#rgMsg"); const btn = $("#rgStart");
  btn.disabled = true; m.textContent = "기동 중…"; m.className = "msg";
  try {
    const r = await api("/api/rclone-gui", { method: "POST", body: JSON.stringify({ Action: "start" }) });
    if (r.ok) {
      m.textContent = ""; rgShowModal(true); rgWaitAndLoad();
    } else { m.textContent = "✕ " + ((await r.json()).error || r.status); m.className = "msg fail"; }
  } catch (e) { if (String(e.message) !== "unauthorized") { m.textContent = "✕ " + e.message; m.className = "msg fail"; } }
  btn.disabled = false;
}
async function rgClose() {
  rgShowModal(false);
  try { await api("/api/rclone-gui", { method: "POST", body: JSON.stringify({ Action: "stop" }) }); } catch (e) {}
  loadRemotes();
}
function rgReload() { $("#rgLoading").style.display = ""; $("#rgFrame").src = "/rclone-gui/?v=" + Date.now(); }
function rgStatus() { /* status not shown in card anymore; modal-driven */ }

/* ---------- excludes ---------- */
async function loadExcludes() {
  const t = await (await api("/api/excludes")).text();
  $("#excludes").value = t;
}
async function saveExcludes() {
  const m = $("#exMsg");
  const r = await api("/api/excludes", { method: "PUT", headers: { "Content-Type": "text/plain" }, body: $("#excludes").value });
  if (r.ok) { m.textContent = "✓ 저장됨 (다음 백업부터 적용)"; m.className = "msg ok"; }
  else { m.textContent = "✕ 실패 (" + r.status + ")"; m.className = "msg fail"; }
}

/* ---------- restore ---------- */
async function restore() {
  const snap = $("#rsnap").value;
  const pass = $("#rpass").value;
  const paths = $("#rpaths").value.split(/[\n,]/).map(s => s.trim()).filter(Boolean);
  const msg = $("#rmsg");
  if (!snap) { msg.textContent = "스냅샷을 선택하세요"; msg.className = "msg fail"; return; }
  if (!pass) { msg.textContent = "비밀번호를 입력하세요"; msg.className = "msg fail"; return; }
  const btn = $("#restoreBtn");
  btn.disabled = true; msg.textContent = "복원 중… (크기에 따라 시간이 걸립니다)"; msg.className = "msg";
  $("#rdl").style.display = "none";
  try {
    const r = await api("/api/restore", { method: "POST", body: JSON.stringify({ Snapshot: snap, Includes: paths, Password: pass, Confirm: "RESTORE" }) });
    if (r.ok) {
      const d = await r.json();
      msg.textContent = "✓ 복원 완료 → " + d.target; msg.className = "msg ok";
      $("#rpass").value = ""; $("#rdl").style.display = "";
    } else if (r.status === 401) {
      msg.textContent = "✕ 비밀번호가 올바르지 않습니다"; msg.className = "msg fail";
    } else if (r.status === 409) {
      msg.textContent = "✕ 다른 작업 진행 중"; msg.className = "msg fail";
    } else {
      msg.textContent = "✕ " + (await r.text()); msg.className = "msg fail";
    }
  } catch (e) {
    if (String(e.message) !== "unauthorized") { msg.textContent = "✕ " + e.message; msg.className = "msg fail"; }
  }
  btn.disabled = false;
}
function downloadRestore() { window.location.href = "/api/restore-download"; }

async function logout() { try { await api("/logout", { method: "POST" }); } catch (e) {} toLogin(); }

$("#saveCfg").onclick = saveCfg;
$("#saveExcludes").onclick = saveExcludes;
$("#rgStart").onclick = rgStart;
$("#rgClose").onclick = rgClose;
$("#rgReload").onclick = rgReload;
$("#rmRefresh").onclick = loadRemotes;
$("#addType").onchange = renderAddFields;
$("#addBtn").onclick = addRemote;
renderAddFields();
$("#backupNow").onclick = backup;
$("#restoreBtn").onclick = restore;
$("#rdl").onclick = downloadRestore;
$("#logout").onclick = logout;

function failCard(sel, e) {
  if (String(e && e.message) === "unauthorized") return; // redirecting to /login
  const el = $(sel);
  if (el) el.innerHTML = `<div class="empty" style="color:var(--fail)">불러오기 실패: ${esc(e && e.message || e)}</div>`;
}

(async () => {
  const b = $("#build"); if (b) b.textContent = BUILD;
  initTabs();
  getCsrf();
  // Fast panels render immediately; the slow snapshot list (restic→Drive) loads
  // in the background and updates the strip count when ready.
  $("#snaps").innerHTML = '<tbody><tr><td class="empty">불러오는 중…</td></tr></tbody>';
  $("#config").innerHTML = '<div class="empty">불러오는 중…</div>';
  loadStatus().catch(e => failCard("#strip", e));
  loadConfig().catch(e => failCard("#config", e));
  loadExcludes().catch(() => {});
  rgStatus();
  loadRemotes();
  loadHistory().catch(e => failCard("#history", e));
  loadSnaps().then(() => { if (window._busyAtLoad) pollUntilIdle(); }).catch(e => failCard("#snaps", e));
})();
