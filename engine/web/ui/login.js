const $ = s => document.querySelector(s);

async function login() {
  $("#loginErr").textContent = "";
  const r = await fetch("/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ User: $("#u").value, Pass: $("#p").value })
  });
  if (r.ok) {
    location.href = "/";
  } else {
    $("#loginErr").textContent = r.status === 401 ? "아이디 또는 비밀번호가 올바르지 않습니다." : ("로그인 실패 (" + r.status + ")");
  }
}

$("#loginBtn").onclick = login;
["u", "p"].forEach(id => $("#" + id).addEventListener("keydown", e => { if (e.key === "Enter") login(); }));
$("#u").focus();
