package webserver

// baseCSS is the shared stylesheet for the status page ("/") and the config
// editor ("/config"), kept in one place so the two pages read as one
// application rather than two independently styled fragments. It's a plain
// Go string constant (not a template) so both the html/template-rendered
// status page and the static configPageHTML const can concatenate it in at
// compile time.
const baseCSS = `
:root {
  --bg: #f6f7f8; --card: #ffffff; --border: #e1e3e6; --border-strong: #c7cad0;
  --text: #1a1a1a; --text-muted: #666; --accent: #1a7f37; --accent-dark: #146029;
  --danger: #c0392b; --danger-bg: #fbeaea; --ok-bg: #e6f4ea;
  --radius: 8px; --radius-sm: 4px;
  --shadow: 0 1px 2px rgba(0,0,0,0.04), 0 1px 8px rgba(0,0,0,0.03);
}
* { box-sizing: border-box; }
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  margin: 0; background: var(--bg); color: var(--text); line-height: 1.45;
}
a { color: var(--accent); text-decoration: none; }
a:hover { text-decoration: underline; }

.topbar {
  display: flex; align-items: center; justify-content: space-between;
  padding: 0.75rem 1.5rem; background: var(--card); border-bottom: 1px solid var(--border);
  position: sticky; top: 0; z-index: 10;
}
.topbar .brand { font-weight: 700; font-size: 1.05rem; color: var(--text); }
.topbar nav { display: flex; gap: 1.25rem; font-size: 0.9rem; }
.topbar nav a { color: var(--text-muted); font-weight: 500; padding: 0.3rem 0; }
.topbar nav a.active { color: var(--accent); border-bottom: 2px solid var(--accent); }

main { max-width: 64rem; margin: 0 auto; padding: 1.5rem; }

.tabs { display: flex; gap: 0.2rem; border-bottom: 1px solid var(--border); margin-bottom: 1.1rem; overflow-x: auto; }
.tab-btn { background: none; border: none; border-bottom: 2px solid transparent; padding: 0.6rem 1rem; font: inherit; font-size: 0.88rem; font-weight: 500; color: var(--text-muted); cursor: pointer; white-space: nowrap; border-radius: 0; }
.tab-btn:hover { color: var(--text); }
.tab-btn.active { color: var(--accent); border-bottom-color: var(--accent); }

.card {
  background: var(--card); border: 1px solid var(--border); border-radius: var(--radius);
  box-shadow: var(--shadow); padding: 1.1rem 1.3rem; margin-bottom: 1.1rem;
}
.card h2 { margin: 0 0 0.3rem; font-size: 1rem; }
.card > .hint:first-of-type { margin-top: 0; margin-bottom: 0.8rem; }

.stat-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr)); gap: 0.8rem; margin-bottom: 1.1rem; }
.stat-card { background: var(--card); border: 1px solid var(--border); border-radius: var(--radius); padding: 0.9rem 1rem; box-shadow: var(--shadow); }
.stat-card .label { font-size: 0.75rem; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.03em; }
.stat-card .value { font-size: 1.4rem; font-weight: 700; margin-top: 0.25rem; }
.stat-card .sub { margin-top: 0.3rem; }

table { border-collapse: collapse; width: 100%; font-size: 0.88rem; }
th, td { border-bottom: 1px solid var(--border); padding: 0.5rem 0.6rem; text-align: left; }
th { color: var(--text-muted); font-weight: 600; font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.02em; }
tr:last-child td { border-bottom: none; }
td .sync-map-btn { padding: 0.25rem 0.6rem; font-size: 0.78rem; }

.badge { display: inline-block; padding: 0.15rem 0.55rem; border-radius: 999px; font-size: 0.78rem; font-weight: 600; }
.badge.ok { background: var(--ok-bg); color: var(--accent-dark); }
.badge.err { background: var(--danger-bg); color: var(--danger); }

pre.log { background: #111418; color: #d8dee3; padding: 1rem; border-radius: var(--radius-sm); overflow-x: auto; max-height: 50vh; font-size: 0.82rem; margin: 0.6rem 0 0; }

details.log-card > summary { cursor: pointer; font-weight: 600; font-size: 0.9rem; list-style: none; }
details.log-card > summary::-webkit-details-marker { display: none; }
details.log-card > summary::before { content: "▸ "; color: var(--text-muted); }
details.log-card[open] > summary::before { content: "▾ "; }

fieldset { border: none; padding: 0; margin: 0; }
label { display: block; font-size: 0.83rem; font-weight: 500; margin: 0.7rem 0 0.2rem; color: #333; }
input[type=text], input[type=password], textarea {
  width: 100%; font: inherit; font-size: 0.9rem; padding: 0.4rem 0.55rem;
  border: 1px solid var(--border-strong); border-radius: var(--radius-sm); background: #fff;
}
input:focus, textarea:focus { outline: 2px solid #b9dcc3; outline-offset: 0; border-color: var(--accent); }
input:disabled { background: #f2f2f3; color: #888; }
textarea { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.85rem; resize: vertical; }

.checkbox-row { display: flex; align-items: center; gap: 0.5rem; margin: 0.8rem 0 0.3rem; }
.checkbox-row label { margin: 0; font-weight: 500; }

.col-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr)); gap: 0.6rem 1rem; margin-top: 0.6rem; }

button {
  font: inherit; font-size: 0.85rem; padding: 0.45rem 0.9rem; border-radius: var(--radius-sm);
  border: 1px solid var(--border-strong); background: #f8f8f9; color: var(--text); cursor: pointer;
}
button:hover { filter: brightness(0.97); }
button.primary { background: var(--accent); color: #fff; border-color: var(--accent); }
button.primary:hover { background: var(--accent-dark); }
button.danger { background: #fff; color: var(--danger); border-color: var(--danger); }
button:disabled { opacity: 0.6; cursor: default; }

#msg.banner { padding: 0.7rem 1rem; border-radius: var(--radius-sm); margin-bottom: 1.1rem; display: none; font-size: 0.9rem; }
#msg.banner.ok { display: block; background: var(--ok-bg); color: var(--accent-dark); }
#msg.banner.err { display: block; background: var(--danger-bg); color: var(--danger); white-space: pre-wrap; }

.hint { color: var(--text-muted); font-size: 0.8rem; margin: 0.2rem 0 0; }

details.map-card { border: 1px solid var(--border); border-radius: var(--radius-sm); margin-bottom: 0.7rem; background: #fbfbfc; }
details.map-card > summary {
  cursor: pointer; padding: 0.6rem 0.9rem; font-weight: 600; font-size: 0.9rem;
  list-style: none; display: flex; align-items: center; gap: 0.6rem;
}
details.map-card > summary::-webkit-details-marker { display: none; }
details.map-card > summary::before { content: "▸"; color: var(--text-muted); transition: transform 0.15s; flex: none; }
details.map-card[open] > summary::before { transform: rotate(90deg); }
.map-summary-id { font-weight: 600; }
.map-summary-meta { font-weight: 400; color: var(--text-muted); font-size: 0.8rem; }
details.map-card .map-body { padding: 0.2rem 0.9rem 0.9rem; }

.actions-row { display: flex; gap: 0.6rem; margin-top: 1.4rem; }

@media (max-width: 40rem) {
  main { padding: 1rem; }
  .topbar { padding: 0.6rem 1rem; }
}
`
