package webserver

// setupPageHTML is served at GET /setup, only reachable while no account
// exists yet (see setupGate in auth.go). Creating an account here always
// grants every permission plus superuser, since there's no one else yet to
// have granted anything more selectively (see allPermissions/setupHandler).
const setupPageHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<script>` + themeInitScript + `</script>
<title>go-sync-objects setup</title>
<style>` + baseCSS + `</style>
</head>
<body>
<button type="button" id="theme-toggle" class="theme-toggle standalone" aria-label="Toggle dark mode"></button>
<main style="max-width:24rem; margin-top:3.5rem;">
<div class="card">
  <h2 style="margin-bottom:0.3rem;">Create the first account</h2>
  <p class="hint" style="margin-bottom:0.9rem;">No accounts exist yet. The account you create here
  is a full administrator, with every permission and access to user management &mdash; you can create
  more restricted accounts afterwards from the Users page.</p>
  <div id="msg" class="banner"></div>
  <form id="setup-form">
    <label for="username">Username</label>
    <input type="text" id="username" name="username" autocomplete="username" required>
    <label for="password">Password</label>
    <input type="password" id="password" name="password" autocomplete="new-password" required>
    <div class="actions-row">
      <button type="submit" class="primary">Create account</button>
    </div>
  </form>
</div>
</main>
<script>` + themeToggleJS + `</script>
<script>
(function () {
  "use strict";

  initThemeToggle();

  var msg = document.getElementById("msg");
  function showMessage(text) {
    msg.className = "banner err";
    msg.textContent = text;
  }

  document.getElementById("setup-form").addEventListener("submit", function (ev) {
    ev.preventDefault();

    var body = new URLSearchParams();
    body.set("username", document.getElementById("username").value);
    body.set("password", document.getElementById("password").value);

    fetch("/setup", { method: "POST", body: body })
      .then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
      .then(function (res) {
        if (res.ok) {
          location.href = res.body.redirect || "/";
        } else {
          showMessage(res.body.error || "Setup failed.");
        }
      }).catch(function (e) { showMessage("Setup failed: " + e); });
  });
})();
</script>
</body>
</html>
`
