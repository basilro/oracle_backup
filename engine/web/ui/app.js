let csrf = "";
const $ = s => document.querySelector(s);
const cfgFields = {
  keep_daily: "보존일수", upload_limit_kbps: "업로드 제한(KB/s)",
  backup_schedule: "백업 스케줄(cron)", check_schedule: "검증 스케줄(cron)",
  scheduler_enabled: "스케줄러 사용", db_backup_enabled: "DB 백업", min_free_mb: "최소 여유(MB)"
};

function getCsrf() {
  const m = document.cookie.match(/csrf=([^;]+)/);
  if (m) csrf = m[1];
}
async function api(p, o = {}) {
  o.headers = Object.assign({ "Content-Type": "application/json", "X-CSRF-Token": csrf }, o.headers || {});
  return fetch(p, o);
}

async function login() {
  const r = await api("/login", { method: "POST", body: JSON.stringify({ User: $("#u").value, Pass: $("#p").value }) });
  if (r.ok) { csrf = (await r.json()).csrf; show(); }
  else { $("#loginErr").textContent = "로그인 실패"; }
}

async function show() {
  getCsrf();
  $("#login").hidden = true; $("#dash").hidden = false; $("#logout").hidden = false;
  await Promise.all([loadStatus(), loadConfig(), loadSnaps(), loadHistory()]);
}

async function loadStatus() {
  const r = await api("/api/status");
  if (r.status === 401) { location.reload(); return; }
  const s = await r.json();
  $("#status").innerHTML = `<h2>상태</h2>마지막 성공: <b>${s.last_success || "-"}</b><br>다음 예정: ${s.next_run || "(스케줄러 꺼짐)"}<br>진행 중: ${s.busy ? "예" : "아니오"}` +
    (s.last_failure ? `<br><span class="fail">마지막 실패: ${s.last_failure}</span>` : "");
}

async function loadConfig() {
  const c = await (await api("/api/config")).json();
  window._cfg = c;
  $("#config").innerHTML = Object.entries(cfgFields).map(([k, l]) =>
    `<label>${l}: <input data-k="${k}" value="${c[k]}"></label>`).join("<br>");
}

async function saveCfg() {
  const v = {};
  document.querySelectorAll("#config input").forEach(i => { v[i.dataset.k] = i.value; });
  const body = {
    KeepDaily: parseInt(v.keep_daily), UploadLimit: parseInt(v.upload_limit_kbps), MinFreeMB: parseInt(v.min_free_mb),
    BackupSchedule: v.backup_schedule, CheckSchedule: v.check_schedule,
    SchedulerEnabled: v.scheduler_enabled === "true", DBBackupEnabled: v.db_backup_enabled === "true"
  };
  const r = await api("/api/config", { method: "PUT", body: JSON.stringify(body) });
  $("#cfgMsg").textContent = r.ok ? "저장됨" : ("실패: " + (await r.text()));
  $("#cfgMsg").className = r.ok ? "ok" : "fail";
  loadStatus();
}

async function loadSnaps() {
  const s = await (await api("/api/snapshots")).json();
  const rows = Array.isArray(s) ? s.slice().reverse().map(x =>
    `<tr><td>${(x.time || "").slice(0, 19)}</td><td>${x.short_id || (x.id || "").slice(0, 8)}</td><td>${(x.tags || []).join(",")}</td><td>${(x.paths || []).join("<br>")}</td></tr>`).join("") : "";
  $("#snaps").innerHTML = "<tr><th>시각</th><th>ID</th><th>태그</th><th>경로</th></tr>" + rows;
}

function fmtB(n) {
  if (!n) return "-";
  const u = ["B", "KB", "MB", "GB"]; let i = 0;
  while (n >= 1024 && i < 3) { n /= 1024; i++; }
  return n.toFixed(1) + u[i];
}

async function loadHistory() {
  const h = await (await api("/api/history")).json();
  const rows = Array.isArray(h) ? h.map(r =>
    `<tr><td>${r.StartedAt}</td><td>${r.Trigger}</td><td class="${r.Status === "ok" ? "ok" : "fail"}">${r.Status}</td><td>${fmtB(r.DataAdded)}</td></tr>`).join("") : "";
  $("#history").innerHTML = "<tr><th>시작</th><th>트리거</th><th>상태</th><th>추가량</th></tr>" + rows;
}

async function backup() {
  const r = await api("/api/backup", { method: "POST" });
  $("#actMsg").textContent = r.status === 202 ? "백업 시작됨" : (r.status === 409 ? "이미 진행 중" : "실패");
  setTimeout(() => { loadStatus(); loadHistory(); }, 3000);
}

async function logout() { await api("/logout", { method: "POST" }); location.reload(); }

$("#loginBtn").onclick = login;
$("#saveCfg").onclick = saveCfg;
$("#backupNow").onclick = backup;
$("#logout").onclick = logout;
$("#p").addEventListener("keydown", e => { if (e.key === "Enter") login(); });

(async () => {
  getCsrf();
  const r = await api("/api/status");
  if (r.ok) show();
})();
