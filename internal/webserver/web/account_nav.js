
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
    var navSecurityLog = document.getElementById("nav-security-log");
    if (navSecurityLog) navSecurityLog.style.display = me.isSuperuser ? "" : "none";
    if (onMe) onMe(me);
  }).catch(function () {});
}
