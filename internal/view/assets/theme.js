(function () {
  var root = document.documentElement;
  var key = "beerus-theme";
  try {
    var saved = localStorage.getItem(key);
    if (saved === "light" || saved === "dark") root.setAttribute("data-theme", saved);
  } catch (e) {}
  function current() {
    var t = root.getAttribute("data-theme");
    if (t) return t;
    return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
  }
  function label() {
    var next = current() === "light" ? "dark" : "light";
    var text = "Switch to " + next + " mode";
    var buttons = document.querySelectorAll(".theme-toggle");
    for (var i = 0; i < buttons.length; i++) {
      buttons[i].setAttribute("aria-label", text);
      buttons[i].setAttribute("title", text);
    }
  }
  document.addEventListener("click", function (e) {
    var btn = e.target.closest && e.target.closest(".theme-toggle");
    if (!btn) return;
    var next = current() === "light" ? "dark" : "light";
    root.setAttribute("data-theme", next);
    try {
      localStorage.setItem(key, next);
    } catch (err) {}
    label();
  });
  document.addEventListener("DOMContentLoaded", label);
})();
