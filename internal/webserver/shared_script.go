package webserver

// accountNavJS is shared by every logged-in page (status, config, users): it
// fetches the current user from GET /api/me, fills in the topbar's
// "#account-nav" with the username and a Logout control, and toggles the
// "#nav-config"/"#nav-users" links based on the user's permissions/
// superuser flag. It's concatenated as its own <script> block ahead of each
// page's own inline script (which may call initAccountNav's callback to do
// page-specific permission-driven UI, e.g. disabling a config tab's save
// button) — this mirrors how baseCSS is concatenated into every page's
// <style> block in styles.go, keeping one shared source of truth in Go
// rather than duplicating this logic across the hand-written HTML constants.
const accountNavJS = `
function initAccountNav(onMe) {
  "use strict";
  fetch("/api/me").then(function (r) { return r.json(); }).then(function (me) {
    var nav = document.getElementById("account-nav");
    if (nav) {
      var span = document.createElement("span");
      span.textContent = me.username + " ";
      nav.appendChild(span);
      var logout = document.createElement("a");
      logout.href = "#";
      logout.textContent = "Logout";
      logout.addEventListener("click", function (ev) {
        ev.preventDefault();
        fetch("/logout", { method: "POST" }).then(function () { location.href = "/login"; });
      });
      nav.appendChild(logout);
    }
    var navConfig = document.getElementById("nav-config");
    if (navConfig) navConfig.style.display = (me.permissions && me.permissions.viewConfig) ? "" : "none";
    var navUsers = document.getElementById("nav-users");
    if (navUsers) navUsers.style.display = me.isSuperuser ? "" : "none";
    if (onMe) onMe(me);
  }).catch(function () {});
}
`

// themeInitScript sets the "data-theme" attribute on <html> from any
// explicit choice saved in localStorage (see themeToggleJS below), before
// the rest of the page renders. It's inlined as its own <script> tag right
// after <meta charset> in every page's <head> — ahead of baseCSS's <style>
// tag and everything in <body> — so the correct palette (light/dark
// override CSS in styles.go keys off this attribute) applies on first
// paint instead of flashing the OS-default theme and then switching.
const themeInitScript = `
(function () {
  "use strict";
  try {
    var t = localStorage.getItem("theme");
    if (t === "dark" || t === "light") {
      document.documentElement.setAttribute("data-theme", t);
    }
  } catch (e) { /* localStorage unavailable (private mode, etc.) - fall back to OS preference */ }
})();
`

// themeToggleJS defines initThemeToggle, called by every page (standalone on
// /login and /setup, alongside initAccountNav elsewhere) to wire up that
// page's "#theme-toggle" button: it reflects the current effective theme
// (explicit data-theme attribute, falling back to the OS media query) in
// the button's label, and flips + persists an explicit choice on click.
const themeToggleJS = `
function initThemeToggle() {
  "use strict";
  var btn = document.getElementById("theme-toggle");
  if (!btn) return;

  function current() {
    var attr = document.documentElement.getAttribute("data-theme");
    if (attr === "dark" || attr === "light") return attr;
    return (window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches) ? "dark" : "light";
  }
  function render() {
    btn.textContent = current() === "dark" ? "Light mode" : "Dark mode";
  }

  render();
  btn.addEventListener("click", function () {
    var next = current() === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-theme", next);
    try { localStorage.setItem("theme", next); } catch (e) { /* ignore */ }
    render();
  });
}
`
