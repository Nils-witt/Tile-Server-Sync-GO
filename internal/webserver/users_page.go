package webserver

import "net/http"

func usersPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/users" {
		http.NotFound(w, r)
		return
	}

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(usersPageHTML))
}

// usersPageHTML is served at GET /users, reachable only by superusers (see
// requireSuperuser in webserver.go). Like configPageHTML, it's a static page
// driven entirely by fetch calls to /api/users, with no server-side
// templating.
const usersPageHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<script>` + themeInitScript + `</script>
<title>go-sync-objects users</title>
<style>` + baseCSS + `</style>
</head>
<body>
<div class="topbar">
  <span class="brand">go-sync-objects</span>
  <nav>
    <a href="/">Status</a>
    <a href="/config" id="nav-config">Config</a>
    <a href="/users" class="active" id="nav-users">Users</a>
    <a href="/security-log" id="nav-security-log">Security log</a>
  </nav>
  <nav id="account-nav"></nav>
  <button type="button" id="theme-toggle" class="theme-toggle" aria-label="Toggle dark mode"></button>
</div>

<main>

<div id="msg" class="banner"></div>

<div class="card">
  <h2>Users</h2>
  <p class="hint">Superuser controls user management only &mdash; it does not by itself grant any of
  the six feature permissions below, and vice versa.</p>
  <table id="users-table">
    <tr>
      <th>Username</th><th>Superuser</th><th>View status</th><th>Trigger sync</th><th>View config</th>
      <th>Edit API</th><th>Edit database</th><th>Edit maps</th><th>Edit SSO</th><th></th>
    </tr>
  </table>
</div>

<div class="card">
  <h2>Add user</h2>
  <label for="new-username">Username</label>
  <input type="text" id="new-username">
  <label for="new-password">Password</label>
  <input type="password" id="new-password">
  <div class="checkbox-row"><input type="checkbox" id="new-superuser"><label for="new-superuser">Superuser (user management)</label></div>
  <div class="checkbox-row"><input type="checkbox" id="new-view-status"><label for="new-view-status">View status</label></div>
  <div class="checkbox-row"><input type="checkbox" id="new-trigger-sync"><label for="new-trigger-sync">Trigger sync</label></div>
  <div class="checkbox-row"><input type="checkbox" id="new-view-config"><label for="new-view-config">View config</label></div>
  <div class="checkbox-row"><input type="checkbox" id="new-edit-api"><label for="new-edit-api">Edit config: API</label></div>
  <div class="checkbox-row"><input type="checkbox" id="new-edit-database"><label for="new-edit-database">Edit config: Database</label></div>
  <div class="checkbox-row"><input type="checkbox" id="new-edit-maps"><label for="new-edit-maps">Edit config: Maps</label></div>
  <div class="checkbox-row"><input type="checkbox" id="new-edit-sso"><label for="new-edit-sso">Edit config: SSO</label></div>
  <div class="actions-row">
    <button type="button" class="primary" id="add-user">Add user</button>
  </div>
</div>

</main>

<script>` + accountNavJS + themeToggleJS + `</script>
<script>
(function () {
  "use strict";

  initThemeToggle();

  var msg = document.getElementById("msg");
  function showMessage(ok, text) {
    msg.className = "banner " + (ok ? "ok" : "err");
    msg.textContent = text;
  }

  function permCheckbox(id, checked, onChange) {
    var td = document.createElement("td");
    var input = document.createElement("input");
    input.type = "checkbox";
    input.checked = checked;
    input.addEventListener("change", function () { onChange(input.checked); });
    td.appendChild(input);
    return td;
  }

  function saveUser(id, patch) {
    fetch("/api/users/" + encodeURIComponent(id), {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(patch)
    }).then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
      .then(function (res) {
        if (res.ok) {
          showMessage(true, "Saved.");
        } else {
          showMessage(false, "Failed to save: " + res.body.error);
          load();
        }
      }).catch(function (e) { showMessage(false, "Failed to save: " + e); });
  }

  function buildRow(u) {
    var tr = document.createElement("tr");

    var nameTd = document.createElement("td");
    nameTd.textContent = u.username;
    tr.appendChild(nameTd);

    function patchOf(field, value) {
      var patch = {
        isSuperuser: u.isSuperuser,
        permissions: Object.assign({}, u.permissions),
        password: ""
      };
      if (field === "isSuperuser") {
        patch.isSuperuser = value;
      } else {
        patch.permissions[field] = value;
      }
      return patch;
    }

    tr.appendChild(permCheckbox("superuser", u.isSuperuser, function (v) { saveUser(u.id, patchOf("isSuperuser", v)); }));
    tr.appendChild(permCheckbox("viewStatus", u.permissions.viewStatus, function (v) { saveUser(u.id, patchOf("viewStatus", v)); }));
    tr.appendChild(permCheckbox("triggerSync", u.permissions.triggerSync, function (v) { saveUser(u.id, patchOf("triggerSync", v)); }));
    tr.appendChild(permCheckbox("viewConfig", u.permissions.viewConfig, function (v) { saveUser(u.id, patchOf("viewConfig", v)); }));
    tr.appendChild(permCheckbox("editConfigAPI", u.permissions.editConfigAPI, function (v) { saveUser(u.id, patchOf("editConfigAPI", v)); }));
    tr.appendChild(permCheckbox("editConfigDatabase", u.permissions.editConfigDatabase, function (v) { saveUser(u.id, patchOf("editConfigDatabase", v)); }));
    tr.appendChild(permCheckbox("editConfigMaps", u.permissions.editConfigMaps, function (v) { saveUser(u.id, patchOf("editConfigMaps", v)); }));
    tr.appendChild(permCheckbox("editConfigSSO", u.permissions.editConfigSSO, function (v) { saveUser(u.id, patchOf("editConfigSSO", v)); }));

    var actionsTd = document.createElement("td");
    var delBtn = document.createElement("button");
    delBtn.type = "button";
    delBtn.className = "danger";
    delBtn.textContent = "Delete";
    delBtn.addEventListener("click", function () {
      if (!confirm("Delete user \"" + u.username + "\"?")) return;
      fetch("/api/users/" + encodeURIComponent(u.id), { method: "DELETE" })
        .then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
        .then(function (res) {
          if (res.ok) { load(); } else { showMessage(false, "Failed to delete: " + res.body.error); }
        }).catch(function (e) { showMessage(false, "Failed to delete: " + e); });
    });
    actionsTd.appendChild(delBtn);
    tr.appendChild(actionsTd);

    return tr;
  }

  function load() {
    fetch("/api/users").then(function (r) { return r.json(); }).then(function (users) {
      var table = document.getElementById("users-table");
      table.querySelectorAll("tr:not(:first-child)").forEach(function (tr) { tr.remove(); });
      (users || []).forEach(function (u) { table.appendChild(buildRow(u)); });
    }).catch(function (e) { showMessage(false, "Failed to load users: " + e); });
  }

  document.getElementById("add-user").addEventListener("click", function () {
    var body = {
      username: document.getElementById("new-username").value,
      password: document.getElementById("new-password").value,
      isSuperuser: document.getElementById("new-superuser").checked,
      permissions: {
        viewStatus: document.getElementById("new-view-status").checked,
        triggerSync: document.getElementById("new-trigger-sync").checked,
        viewConfig: document.getElementById("new-view-config").checked,
        editConfigAPI: document.getElementById("new-edit-api").checked,
        editConfigDatabase: document.getElementById("new-edit-database").checked,
        editConfigMaps: document.getElementById("new-edit-maps").checked,
        editConfigSSO: document.getElementById("new-edit-sso").checked
      }
    };

    fetch("/api/users", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    }).then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
      .then(function (res) {
        if (res.ok) {
          document.getElementById("new-username").value = "";
          document.getElementById("new-password").value = "";
          showMessage(true, "User created.");
          load();
        } else {
          showMessage(false, "Failed to create user: " + res.body.error);
        }
      }).catch(function (e) { showMessage(false, "Failed to create user: " + e); });
  });

  initAccountNav();

  load();
})();
</script>
</body>
</html>
`
