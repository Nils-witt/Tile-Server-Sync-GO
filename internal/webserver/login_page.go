package webserver

// loginPageHTML is served at GET /login. It works as a plain HTML form
// (no JavaScript required) and also progressively enhances itself with a
// fetch-based submit so a failed login doesn't need a full page reload.
const loginPageHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<script>` + themeInitScript + `</script>
<title>go-sync-objects login</title>
<style>` + baseCSS + `</style>
</head>
<body>
<button type="button" id="theme-toggle" class="theme-toggle standalone" aria-label="Toggle dark mode"></button>
<main style="max-width:22rem; margin-top:3.5rem;">
<div class="card">
  <h2 style="margin-bottom:0.9rem;">Sign in</h2>
  <div id="msg" class="banner"></div>
  <form id="login-form" method="post" action="/login">
    <label for="username">Username</label>
    <input type="text" id="username" name="username" autocomplete="username" required>
    <label for="password">Password</label>
    <input type="password" id="password" name="password" autocomplete="current-password" required>
    <input type="hidden" id="next" name="next">
    <div class="actions-row">
      <button type="submit" class="primary">Sign in</button>
    </div>
  </form>
</div>
</main>
<script>` + themeToggleJS + `</script>
<script>
(function () {
  "use strict";

  initThemeToggle();

  var params = new URLSearchParams(location.search);
  document.getElementById("next").value = params.get("next") || "";

  var msg = document.getElementById("msg");
  function showMessage(text) {
    msg.className = "banner err";
    msg.textContent = text;
  }

  document.getElementById("login-form").addEventListener("submit", function (ev) {
    ev.preventDefault();

    fetch("/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        username: document.getElementById("username").value,
        password: document.getElementById("password").value,
        next: document.getElementById("next").value
      })
    }).then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
      .then(function (res) {
        if (res.ok) {
          location.href = res.body.redirect || "/";
        } else {
          showMessage(res.body.error || "Sign in failed.");
        }
      }).catch(function (e) { showMessage("Sign in failed: " + e); });
  });
})();
</script>
</body>
</html>
`
