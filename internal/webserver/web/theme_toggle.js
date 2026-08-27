
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
