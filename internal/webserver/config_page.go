package webserver

// configPageHTML is the config editor page served at GET /config. It is a
// static page (unlike the status page, it doesn't need server-side
// templating: all data comes from and goes to /api/config via fetch), styled
// to match the status page. The script builds DOM nodes with
// createElement/textContent rather than innerHTML for any server-supplied
// value, since that value is attacker-controllable if configPath is ever
// edited by something untrusted.
const configPageHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>go-sync-objects config</title>
<style>
  body { font-family: system-ui, sans-serif; margin: 2rem; color: #1a1a1a; max-width: 60rem; }
  h1 { font-size: 1.3rem; }
  h2 { font-size: 1.05rem; margin-top: 2rem; }
  fieldset { border: 1px solid #ccc; border-radius: 4px; margin: 0 0 1rem; padding: 0.75rem 1rem; }
  legend { font-weight: 600; padding: 0 0.4rem; }
  label { display: block; font-size: 0.85rem; margin: 0.5rem 0 0.15rem; }
  input[type=text], input[type=password], textarea {
    width: 100%; box-sizing: border-box; font: inherit; font-size: 0.9rem;
    padding: 0.3rem 0.4rem; border: 1px solid #bbb; border-radius: 3px;
  }
  textarea { font-family: ui-monospace, monospace; }
  .checkbox-row { display: flex; align-items: center; gap: 0.4rem; margin: 0.5rem 0; }
  .checkbox-row label { margin: 0; }
  .col-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 0.5rem 1rem; }
  .map-row { border: 1px dashed #bbb; border-radius: 4px; padding: 0.6rem 0.8rem; margin-bottom: 0.6rem; }
  button { font: inherit; font-size: 0.85rem; padding: 0.35rem 0.8rem; border-radius: 4px; border: 1px solid #999; background: #f5f5f5; cursor: pointer; }
  button.primary { background: #1a7f37; color: #fff; border-color: #1a7f37; }
  button.danger { background: #fff; color: #c0392b; border-color: #c0392b; }
  button:hover { filter: brightness(0.95); }
  #msg { padding: 0.6rem 0.8rem; border-radius: 4px; margin: 1rem 0; display: none; }
  #msg.ok { display: block; background: #e6f4ea; color: #1a7f37; }
  #msg.err { display: block; background: #fbeaea; color: #c0392b; white-space: pre-wrap; }
  .hint { color: #666; font-size: 0.8rem; margin: 0.15rem 0 0; }
  .actions { margin-top: 1.5rem; display: flex; gap: 0.5rem; }
</style>
</head>
<body>
<h1>go-sync-objects config</h1>
<p><a href="/">&larr; Back to status</a> &middot; <a href="#" id="toggle-raw">Edit as raw YAML</a></p>
<p class="hint">Saving writes to the config database (comments are not preserved in the raw-YAML view).</p>

<div id="msg"></div>

<p>
  <button type="button" class="primary" id="reload-running">Apply saved config now</button>
  <span class="hint">Makes the running process re-read the file on disk and use it immediately &mdash;
  API credentials, database settings, maps, and interval all take effect right away, no restart
  needed. The web server's own enabled/address setting is the one exception: that still needs a
  restart.</span>
</p>

<p>
  <button type="button" id="sync-now">Sync now</button>
  <span class="hint">Runs a sync immediately using whatever the running process currently has
  loaded, instead of waiting for the next scheduled interval. Save and apply your changes above
  first if you want this sync to use them. May take a while for large maps.</span>
</p>

<div id="structured-editor">
  <fieldset>
    <legend>API</legend>
    <label for="api-baseurl">Base URL</label>
    <input type="text" id="api-baseurl" placeholder="http://localhost:8085">
    <label for="api-username">Username</label>
    <input type="text" id="api-username">
    <label for="api-password">Password</label>
    <input type="text" id="api-password">
    <label for="api-token">Token (used instead of username/password if set)</label>
    <input type="text" id="api-token">
  </fieldset>

  <fieldset>
    <legend>Sync</legend>
    <label for="interval">Interval (Go duration, e.g. "5m"; empty = run once and exit)</label>
    <input type="text" id="interval" placeholder="5m">
  </fieldset>

  <fieldset>
    <legend>Status web server</legend>
    <div class="checkbox-row"><input type="checkbox" id="ws-enabled" disabled><label for="ws-enabled">Enabled</label></div>
    <label for="ws-address">Address</label>
    <input type="text" id="ws-address" placeholder=":8080" disabled>
    <p class="hint">Set via the bootstrap config file's webServer.enabled/address and requires a
    process restart to change &mdash; edits here are not saved.</p>
  </fieldset>

  <fieldset>
    <legend>Database</legend>
    <label for="db-dsn">DSN</label>
    <input type="text" id="db-dsn" placeholder="user:password@tcp(127.0.0.1:3306)/tileserve?parseTime=true">
    <label for="db-table">Table</label>
    <input type="text" id="db-table" placeholder="geo_objects">
    <div class="checkbox-row"><input type="checkbox" id="db-prune"><label for="db-prune">Prune missing rows after each map/version sync</label></div>

    <label>Column mapping (blank = skip that column)</label>
    <div class="col-grid" id="col-grid"></div>
  </fieldset>

  <fieldset>
    <legend>Maps</legend>
    <div id="maps-container"></div>
    <button type="button" id="add-map">+ Add map</button>
  </fieldset>

  <div class="actions">
    <button type="button" class="primary" id="save-structured">Save</button>
    <button type="button" id="reload">Discard changes (reload form)</button>
  </div>
</div>

<div id="raw-editor" hidden>
  <label for="raw-yaml">Raw YAML</label>
  <p class="hint">webServer is shown here for reference only &mdash; edits to it are discarded on save.</p>
  <textarea id="raw-yaml" rows="30" spellcheck="false"></textarea>
  <div class="actions">
    <button type="button" class="primary" id="save-raw">Save</button>
    <button type="button" id="reload-raw">Discard changes (reload form)</button>
  </div>
</div>

<script>
(function () {
  "use strict";

  var COLUMN_FIELDS = [
    ["uuid", "UUID (upsert key)"], ["mapUuid", "Map UUID"], ["version", "Version"],
    ["name", "Name"], ["externalId", "External ID"], ["latitude", "Latitude"],
    ["longitude", "Longitude"], ["street", "Street"], ["housenumber", "House number"],
    ["postcode", "Postcode"], ["city", "City"], ["cityDistrict", "City district"],
    ["createdAt", "Created at"], ["updatedAt", "Updated at"],
    ["createdBy", "Created by"], ["updatedBy", "Updated by"], ["syncedAt", "Synced at (bookkeeping)"]
  ];

  var colGrid = document.getElementById("col-grid");
  COLUMN_FIELDS.forEach(function (f) {
    var wrap = document.createElement("div");
    var label = document.createElement("label");
    label.setAttribute("for", "col-" + f[0]);
    label.textContent = f[1];
    var input = document.createElement("input");
    input.type = "text";
    input.id = "col-" + f[0];
    wrap.appendChild(label);
    wrap.appendChild(input);
    colGrid.appendChild(wrap);
  });

  var mapsContainer = document.getElementById("maps-container");

  function buildMapRow(m) {
    m = m || { id: "", versions: [], staticColumns: {} };

    var row = document.createElement("div");
    row.className = "map-row";

    var idLabel = document.createElement("label");
    idLabel.textContent = "Map ID";
    var idInput = document.createElement("input");
    idInput.type = "text";
    idInput.className = "map-id";
    idInput.value = m.id || "";

    var versionsLabel = document.createElement("label");
    versionsLabel.textContent = "Versions (comma-separated, e.g. current, 3, 4)";
    var versionsInput = document.createElement("input");
    versionsInput.type = "text";
    versionsInput.className = "map-versions";
    versionsInput.value = (m.versions || []).join(", ");

    var colsLabel = document.createElement("label");
    colsLabel.textContent = "Static columns (one key=value per line)";
    var colsArea = document.createElement("textarea");
    colsArea.className = "map-static-columns";
    colsArea.rows = 3;
    var lines = [];
    Object.keys(m.staticColumns || {}).forEach(function (k) {
      lines.push(k + "=" + m.staticColumns[k]);
    });
    colsArea.value = lines.join("\n");

    var removeBtn = document.createElement("button");
    removeBtn.type = "button";
    removeBtn.className = "danger";
    removeBtn.textContent = "Remove map";
    removeBtn.addEventListener("click", function () { row.remove(); });

    row.appendChild(idLabel);
    row.appendChild(idInput);
    row.appendChild(versionsLabel);
    row.appendChild(versionsInput);
    row.appendChild(colsLabel);
    row.appendChild(colsArea);
    row.appendChild(removeBtn);

    return row;
  }

  document.getElementById("add-map").addEventListener("click", function () {
    mapsContainer.appendChild(buildMapRow(null));
  });

  function parseStaticColumns(text) {
    var out = {};
    text.split("\n").forEach(function (line) {
      line = line.trim();
      if (!line) return;
      var i = line.indexOf("=");
      if (i < 0) return;
      out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
    });
    return out;
  }

  function collectMaps() {
    var maps = [];
    mapsContainer.querySelectorAll(".map-row").forEach(function (row) {
      var id = row.querySelector(".map-id").value.trim();
      var versions = row.querySelector(".map-versions").value
        .split(",").map(function (v) { return v.trim(); }).filter(Boolean);
      var staticColumns = parseStaticColumns(row.querySelector(".map-static-columns").value);
      maps.push({ id: id, versions: versions, staticColumns: staticColumns });
    });
    return maps;
  }

  function populateForm(cfg) {
    document.getElementById("api-baseurl").value = cfg.api.baseUrl || "";
    document.getElementById("api-username").value = cfg.api.username || "";
    document.getElementById("api-password").value = cfg.api.password || "";
    document.getElementById("api-token").value = cfg.api.token || "";
    document.getElementById("interval").value = cfg.interval || "";
    document.getElementById("ws-enabled").checked = !!cfg.webServer.enabled;
    document.getElementById("ws-address").value = cfg.webServer.address || "";
    document.getElementById("db-dsn").value = cfg.database.dsn || "";
    document.getElementById("db-table").value = cfg.database.table || "";
    document.getElementById("db-prune").checked = !!cfg.database.pruneMissing;

    var cols = cfg.database.columns || {};
    COLUMN_FIELDS.forEach(function (f) {
      var input = document.getElementById("col-" + f[0]);
      if (input) input.value = cols[f[0]] || "";
    });

    mapsContainer.innerHTML = "";
    (cfg.maps || []).forEach(function (m) { mapsContainer.appendChild(buildMapRow(m)); });
  }

  function collectForm() {
    var cols = {};
    COLUMN_FIELDS.forEach(function (f) {
      cols[f[0]] = document.getElementById("col-" + f[0]).value.trim();
    });

    return {
      api: {
        baseUrl: document.getElementById("api-baseurl").value.trim(),
        username: document.getElementById("api-username").value,
        password: document.getElementById("api-password").value,
        token: document.getElementById("api-token").value.trim()
      },
      interval: document.getElementById("interval").value.trim(),
      // webServer is intentionally omitted: it's fixed by the bootstrap
      // file and the server ignores/overwrites it on save regardless.
      database: {
        dsn: document.getElementById("db-dsn").value.trim(),
        table: document.getElementById("db-table").value.trim(),
        pruneMissing: document.getElementById("db-prune").checked,
        columns: cols
      },
      maps: collectMaps()
    };
  }

  var msg = document.getElementById("msg");
  function showMessage(ok, text) {
    msg.className = ok ? "ok" : "err";
    msg.textContent = text;
  }

  function load() {
    fetch("/api/config").then(function (r) { return r.json(); }).then(function (resp) {
      document.getElementById("raw-yaml").value = resp.raw || "";
      if (resp.config) {
        populateForm(resp.config);
        msg.className = "";
        msg.textContent = "";
      } else {
        showMessage(false, "Failed to load config; showing raw YAML instead.\n" + (resp.error || ""));
        setRawMode(true);
      }
    }).catch(function (e) { showMessage(false, "Failed to load config: " + e); });
  }

  document.getElementById("save-structured").addEventListener("click", function () {
    fetch("/api/config", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ config: collectForm() })
    }).then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
      .then(function (res) {
        if (res.ok) {
          showMessage(true, "Saved. Click \"Apply saved config now\" to use it without restarting.");
          document.getElementById("raw-yaml").value = res.body.raw || "";
        } else {
          showMessage(false, "Not saved: " + res.body.error);
        }
      }).catch(function (e) { showMessage(false, "Failed to save config: " + e); });
  });

  document.getElementById("save-raw").addEventListener("click", function () {
    fetch("/api/config", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ raw: document.getElementById("raw-yaml").value })
    }).then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
      .then(function (res) {
        if (res.ok) {
          showMessage(true, "Saved. Click \"Apply saved config now\" to use it without restarting.");
        } else {
          showMessage(false, "Not saved: " + res.body.error);
        }
      }).catch(function (e) { showMessage(false, "Failed to save config: " + e); });
  });

  document.getElementById("reload-running").addEventListener("click", function () {
    fetch("/api/reload", { method: "POST" })
      .then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
      .then(function (res) {
        if (res.ok) {
          showMessage(true, "Running process now using the saved config (webServer settings still need a restart).");
        } else {
          showMessage(false, "Reload failed: " + res.body.error);
        }
      }).catch(function (e) { showMessage(false, "Failed to trigger reload: " + e); });
  });

  document.getElementById("sync-now").addEventListener("click", function () {
    var btn = document.getElementById("sync-now");
    btn.disabled = true;
    btn.textContent = "Syncing…";
    showMessage(true, "Sync in progress…");

    fetch("/api/sync", { method: "POST" })
      .then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
      .then(function (res) {
        if (res.ok) {
          showMessage(true, "Synced " + res.body.synced + " object(s).");
        } else {
          showMessage(false, "Sync failed: " + res.body.error);
        }
      }).catch(function (e) { showMessage(false, "Failed to trigger sync: " + e); })
      .then(function () {
        btn.disabled = false;
        btn.textContent = "Sync now";
      });
  });

  document.getElementById("reload").addEventListener("click", load);
  document.getElementById("reload-raw").addEventListener("click", load);

  var rawMode = false;
  function setRawMode(on) {
    rawMode = on;
    document.getElementById("structured-editor").hidden = on;
    document.getElementById("raw-editor").hidden = !on;
    document.getElementById("toggle-raw").textContent = on ? "Edit as form" : "Edit as raw YAML";
  }

  document.getElementById("toggle-raw").addEventListener("click", function (e) {
    e.preventDefault();
    setRawMode(!rawMode);
  });

  load();
})();
</script>
</body>
</html>
`
