/*
 * 首屏主题初始化。
 *
 * 独立文件而非内联脚本，让 CSP 保持 script-src 'self'。
 * 任何存储异常都不能阻止渲染，失败时回落到浅色主题。
 */
(function () {
  var theme = "light";
  try {
    var saved = window.localStorage.getItem("passpal.theme");
    if (saved === "light" || saved === "dark") {
      theme = saved;
    } else if (window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches) {
      theme = "dark";
    }
  } catch (e) {
    theme = "light";
  }
  document.documentElement.setAttribute("data-theme", theme);
})();
