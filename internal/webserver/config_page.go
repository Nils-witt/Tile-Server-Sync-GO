package webserver

// configPageHTML is the config editor page served at GET /config. It is a
// static page (unlike the status page, it doesn't need server-side
// templating: all data comes from and goes to /api/config via fetch), styled
// to match the status page via the shared baseCSS. The script builds DOM
// nodes with createElement/textContent rather than innerHTML for any
// server-supplied value, since that value is attacker-controllable if
// configPath is ever edited by something untrusted.
const configPageHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>go-sync-objects config</title>
<style>` + baseCSS + `</style>
</head>
<body>
<div class="topbar">
  <span class="brand">go-sync-objects</span>
  <nav>
    <a href="/">Status</a>
    <a href="/config" class="active">Config</a>
  </nav>
</div>

<main>

<div id="msg" class="banner"></div>

<p class="hint" style="margin-bottom:1.1rem;">Saving writes to the config database and applies it
to the running process immediately &mdash; API credentials, database settings, and maps (including
each map's own interval) all take effect right away, no restart needed. To run a sync immediately
instead of waiting for a map's interval, use the per-map Sync button on the
<a href="/">status page</a>.</p>

<div id="structured-editor">

  <div class="tabs" role="tablist">
    <button type="button" class="tab-btn" data-tab="api" role="tab">API</button>
    <button type="button" class="tab-btn" data-tab="db" role="tab">Database</button>
    <button type="button" class="tab-btn" data-tab="maps" role="tab">Maps</button>
  </div>

  <section class="card tab-panel" id="section-api" data-tab="api">
    <h2>API</h2>
    <p class="hint">Credentials for the tileserve-go instance objects are fetched from.</p>
    <label for="api-baseurl">Base URL</label>
    <input type="text" id="api-baseurl" placeholder="http://localhost:8085">
    <label for="api-username">Username</label>
    <input type="text" id="api-username">
    <label for="api-password">Password</label>
    <input type="text" id="api-password">
    <label for="api-token">Token (used instead of username/password if set)</label>
    <input type="text" id="api-token">
  </section>

  <section class="card tab-panel" id="section-db" data-tab="db">
    <h2>Database</h2>
    <p class="hint">Where synced geo objects are written.</p>
    <label for="db-dsn">DSN</label>
    <input type="text" id="db-dsn" placeholder="user:password@tcp(127.0.0.1:3306)/tileserve?parseTime=true">
    <label for="db-table">Table</label>
    <input type="text" id="db-table" placeholder="geo_objects">
    <div class="checkbox-row"><input type="checkbox" id="db-prune"><label for="db-prune">Prune missing rows after each map/version sync</label></div>

    <details style="margin-top:0.9rem;">
      <summary style="cursor:pointer; font-weight:500; font-size:0.85rem;">Column mapping (advanced &mdash; leave blank to use defaults)</summary>
      <div class="col-grid" id="col-grid"></div>
    </details>
  </section>

  <section class="card tab-panel" id="section-maps" data-tab="maps">
    <h2>Maps</h2>
    <p class="hint">Each map is fetched independently, on its own optional interval.</p>
    <div id="maps-container"></div>
    <div class="actions-row">
      <button type="button" id="add-map">+ Add map</button>
    </div>
  </section>

  <div class="actions-row">
    <button type="button" class="primary" id="save-structured">Save</button>
    <button type="button" id="reload">Discard changes (reload form)</button>
  </div>

</div>

</main>

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

  var tabButtons = Array.prototype.slice.call(document.querySelectorAll(".tab-btn"));
  var tabPanels = Array.prototype.slice.call(document.querySelectorAll(".tab-panel"));

  function activateTab(name) {
    tabButtons.forEach(function (btn) { btn.classList.toggle("active", btn.dataset.tab === name); });
    tabPanels.forEach(function (panel) { panel.hidden = panel.dataset.tab !== name; });
  }

  tabButtons.forEach(function (btn) {
    btn.addEventListener("click", function () {
      activateTab(btn.dataset.tab);
      history.replaceState(null, "", "#" + btn.dataset.tab);
    });
  });

  var initialTab = (location.hash || "").replace("#", "");
  if (!tabButtons.some(function (btn) { return btn.dataset.tab === initialTab; })) {
    initialTab = "api";
  }
  activateTab(initialTab);

  var mapsContainer = document.getElementById("maps-container");

  function summarizeMap(idInput, versionsInput, intervalInput, idSpan, metaSpan) {
    idSpan.textContent = idInput.value.trim() || "(new map)";
    var versions = versionsInput.value.split(",").map(function (v) { return v.trim(); }).filter(Boolean);
    var parts = [];
    parts.push(versions.length === 1 ? "1 version" : versions.length + " versions");
    parts.push(intervalInput.value.trim() ? "every " + intervalInput.value.trim() : "one-shot");
    metaSpan.textContent = parts.join(" · ");
  }

  function buildMapRow(m, openByDefault) {
    m = m || { id: "", versions: [], interval: "", staticColumns: {} };

    var details = document.createElement("details");
    details.className = "map-card";
    details.open = !!openByDefault;

    var summary = document.createElement("summary");
    var idSpan = document.createElement("span");
    idSpan.className = "map-summary-id";
    var metaSpan = document.createElement("span");
    metaSpan.className = "map-summary-meta";
    summary.appendChild(idSpan);
    summary.appendChild(metaSpan);
    details.appendChild(summary);

    var body = document.createElement("div");
    body.className = "map-body";

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

    var intervalLabel = document.createElement("label");
    intervalLabel.textContent = "Interval (Go duration, e.g. \"5m\"; empty = sync once, no automatic repeat)";
    var intervalInput = document.createElement("input");
    intervalInput.type = "text";
    intervalInput.className = "map-interval";
    intervalInput.placeholder = "5m";
    intervalInput.value = m.interval || "";

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
    removeBtn.addEventListener("click", function (ev) {
      ev.preventDefault();
      details.remove();
    });

    body.appendChild(idLabel);
    body.appendChild(idInput);
    body.appendChild(versionsLabel);
    body.appendChild(versionsInput);
    body.appendChild(intervalLabel);
    body.appendChild(intervalInput);
    body.appendChild(colsLabel);
    body.appendChild(colsArea);
    var removeWrap = document.createElement("div");
    removeWrap.className = "actions-row";
    removeWrap.appendChild(removeBtn);
    body.appendChild(removeWrap);

    details.appendChild(body);

    function refreshSummary() { summarizeMap(idInput, versionsInput, intervalInput, idSpan, metaSpan); }
    idInput.addEventListener("input", refreshSummary);
    versionsInput.addEventListener("input", refreshSummary);
    intervalInput.addEventListener("input", refreshSummary);
    refreshSummary();

    return details;
  }

  document.getElementById("add-map").addEventListener("click", function () {
    var row = buildMapRow(null, true);
    mapsContainer.appendChild(row);
    row.scrollIntoView({ behavior: "smooth", block: "nearest" });
    row.querySelector(".map-id").focus();
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
    mapsContainer.querySelectorAll(".map-card").forEach(function (row) {
      var id = row.querySelector(".map-id").value.trim();
      var versions = row.querySelector(".map-versions").value
        .split(",").map(function (v) { return v.trim(); }).filter(Boolean);
      var interval = row.querySelector(".map-interval").value.trim();
      var staticColumns = parseStaticColumns(row.querySelector(".map-static-columns").value);
      maps.push({ id: id, versions: versions, interval: interval, staticColumns: staticColumns });
    });
    return maps;
  }

  function populateForm(cfg) {
    document.getElementById("api-baseurl").value = cfg.api.baseUrl || "";
    document.getElementById("api-username").value = cfg.api.username || "";
    document.getElementById("api-password").value = cfg.api.password || "";
    document.getElementById("api-token").value = cfg.api.token || "";
    document.getElementById("db-dsn").value = cfg.database.dsn || "";
    document.getElementById("db-table").value = cfg.database.table || "";
    document.getElementById("db-prune").checked = !!cfg.database.pruneMissing;

    var cols = cfg.database.columns || {};
    COLUMN_FIELDS.forEach(function (f) {
      var input = document.getElementById("col-" + f[0]);
      if (input) input.value = cols[f[0]] || "";
    });

    mapsContainer.innerHTML = "";
    (cfg.maps || []).forEach(function (m) { mapsContainer.appendChild(buildMapRow(m, false)); });
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
    msg.className = "banner " + (ok ? "ok" : "err");
    msg.textContent = text;
  }

  function load() {
    fetch("/api/config").then(function (r) { return r.json(); }).then(function (resp) {
      if (resp.config) {
        populateForm(resp.config);
        msg.className = "banner";
        msg.textContent = "";
      } else {
        showMessage(false, "Failed to load config.\n" + (resp.error || ""));
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
          if (res.body.applied) {
            showMessage(true, "Saved and applied to the running process.");
          } else {
            showMessage(false, "Saved, but failed to apply to the running process: " + res.body.applyError);
          }
        } else {
          showMessage(false, "Not saved: " + res.body.error);
        }
      }).catch(function (e) { showMessage(false, "Failed to save config: " + e); });
  });

  document.getElementById("reload").addEventListener("click", load);

  load();
})();
</script>
</body>
</html>
`
