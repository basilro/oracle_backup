const BUILD = "ui-2026-06-01c";
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

/* ---------- rclone GUI (loopback only — reach via SSH tunnel) ---------- */
function rgTunnelHelp(port, creds) {
  const host = location.hostname;
  return `보안상 설정 화면은 <b>서버 로컬(127.0.0.1:${esc(port)})에만</b> 열립니다(rclone API가 클라우드 토큰을 다루므로 LAN 노출 금지). 접속 방법:` +
    `<br>1) 내 PC에서 SSH 터널: <code>ssh -L ${esc(port)}:localhost:${esc(port)} &lt;사용자&gt;@${esc(host)}</code>` +
    `<br>2) 브라우저에서 <a href="http://127.0.0.1:${esc(port)}" target="_blank" rel="noopener" style="color:var(--accent)">http://127.0.0.1:${esc(port)}</a> 열기` +
    (creds ? `<br>3) 로그인: <code>${esc(creds.user)}</code> / <code>${esc(creds.pass)}</code>` : "") +
    `<br><span style="color:var(--fail)">끝나면 <b>설정 화면 중지</b> 클릭(미사용 시 30분 후 자동 종료).</span>`;
}
function rgRender(state) {
  const running = state.running;
  $("#rgStart").style.display = running ? "none" : "";
  $("#rgStop").style.display = running ? "" : "none";
  const port = state.port || "5572";
  const loopback = !state.bind || state.bind === "127.0.0.1" || state.bind === "localhost";
  if (running) {
    if (loopback) {
      $("#rgInfo").innerHTML = rgTunnelHelp(port, state.user ? { user: state.user, pass: state.pass || "(시작 시 표시된 비밀번호)" } : null);
    } else {
      const url = `http://${location.hostname}:${port}`;
      const creds = state.user ? `<br>로그인: <code>${esc(state.user)}</code> / <code>${esc(state.pass || "(시작 시 표시된 비밀번호)")}</code>` : "";
      $("#rgInfo").innerHTML = `설정 화면 실행 중 → <a href="${url}" target="_blank" rel="noopener" style="color:var(--accent)">${esc(url)} 열기</a>${creds}` +
        `<br><span class="dim">이 포트(${esc(port)})가 도메인/포워딩으로 열려 있어야 접속됩니다.</span>` +
        `<br><span style="color:var(--fail)">끝나면 <b>설정 화면 중지</b> 클릭(미사용 시 30분 후 자동 종료).</span>`;
    }
  } else {
    $("#rgInfo").innerHTML = `'rclone 설정 열기'를 누르면 rclone 공식 설정 화면이 잠깐 뜹니다.`;
  }
}
async function rgStatus() {
  try { rgRender(await (await api("/api/rclone-gui")).json()); } catch (e) {}
}
async function rgStart() {
  const m = $("#rgMsg"); const btn = $("#rgStart");
  btn.disabled = true; m.textContent = "기동 중… (최초 실행은 GUI 자산 다운로드로 수십 초 걸릴 수 있음)"; m.className = "msg";
  try {
    const r = await api("/api/rclone-gui", { method: "POST", body: JSON.stringify({ Action: "start" }) });
    if (r.ok) {
      const d = await r.json(); rgRender(d); m.textContent = "✓ 실행됨"; m.className = "msg ok";
      const loopback = !d.bind || d.bind === "127.0.0.1" || d.bind === "localhost";
      if (!loopback) window.open(`http://${location.hostname}:${d.port}`, "_blank", "noopener");
    }
    else { m.textContent = "✕ " + (await r.json()).error; m.className = "msg fail"; }
  } catch (e) { if (String(e.message) !== "unauthorized") { m.textContent = "✕ " + e.message; m.className = "msg fail"; } }
  btn.disabled = false;
}
async function rgStop() {
  const m = $("#rgMsg");
  try {
    const r = await api("/api/rclone-gui", { method: "POST", body: JSON.stringify({ Action: "stop" }) });
    if (r.ok) { rgRender({ running: false, port: "5572" }); m.textContent = "✓ 중지됨 (포트 닫힘)"; m.className = "msg ok"; }
    else { m.textContent = "✕ 중지 실패"; m.className = "msg fail"; }
  } catch (e) {}
}

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
$("#rgStop").onclick = rgStop;
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
  getCsrf();
  // Fast panels render immediately; the slow snapshot list (restic→Drive) loads
  // in the background and updates the strip count when ready.
  $("#snaps").innerHTML = '<tbody><tr><td class="empty">불러오는 중…</td></tr></tbody>';
  $("#config").innerHTML = '<div class="empty">불러오는 중…</div>';
  loadStatus().catch(e => failCard("#strip", e));
  loadConfig().catch(e => failCard("#config", e));
  loadExcludes().catch(() => {});
  rgStatus();
  loadHistory().catch(e => failCard("#history", e));
  loadSnaps().then(() => { if (window._busyAtLoad) pollUntilIdle(); }).catch(e => failCard("#snaps", e));
})();
