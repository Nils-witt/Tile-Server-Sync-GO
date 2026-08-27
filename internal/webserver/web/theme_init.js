
(function () {
  "use strict";
  try {
    var t = localStorage.getItem("theme");
    if (t === "dark" || t === "light") {
      document.documentElement.setAttribute("data-theme", t);
    }
  } catch (e) { /* localStorage unavailable (private mode, etc.) - fall back to OS preference */ }
})();
