(function () {
  "use strict";
  var saved = document.cookie.split("; ").find(function (value) {
    return value.indexOf("panda_theme=") === 0;
  });
  var theme = saved ? saved.split("=")[1] : "";
  if (theme !== "light" && theme !== "dark") {
    theme = window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  document.documentElement.dataset.theme = theme;
})();
