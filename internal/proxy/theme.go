package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

// themeProbeMarker 标识注入已存在，避免重复注入。
const themeProbeMarker = "dshDesktopThemeProbe"

// injectThemeProbe 把上报脚本插到 </head> 前（无 </head> 时插到页首）。
// 上报两类信息给壳（iframe 父页面）：
//   1. 主题：检测文档背景亮度，供窗口按钮切换配色；
//   2. 侧栏几何：侧栏宽度变化时上报，供壳动态移动顶部拖动条，
//      避免遮挡侧栏折叠按钮。
func injectThemeProbe(body string) string {
	script := `<script data-` + themeProbeMarker + `>
(function(){
  var last = "";
  function luminance(rgb) {
    var m = rgb.match(/\d+(\.\d+)?/g); if (!m || m.length < 3) return null;
    var c = m.slice(0,3).map(function(v){ v/=255; return v<=0.03928? v/12.92 : Math.pow((v+0.055)/1.055,2.4); });
    return 0.2126*c[0]+0.7152*c[1]+0.0722*c[2];
  }
  function post(msg) {
    try { parent.postMessage(msg, "*"); } catch (e) {}
  }
  function detectTheme() {
    var bg = "";
    var el = document.body || document.documentElement;
    for (var n = el; n; n = n.parentElement) {
      var s = getComputedStyle(n).backgroundColor;
      if (s && s !== "rgba(0, 0, 0, 0)" && s !== "transparent") { bg = s; break; }
    }
    if (!bg && document.documentElement) bg = getComputedStyle(document.documentElement).backgroundColor;
    var l = luminance(bg); var theme = l === null ? "unknown" : (l < 0.45 ? "dark" : "light");
    if (theme !== last) {
      last = theme;
      post({ type: "dsh-desktop-theme", theme: theme, bg: bg });
    }
  }
  /* 侧栏探测：取左侧与视口同高的最高元素列（sidebar 变体多样，
     按"左侧贴边、高度满屏、宽度 100~500px"的特征找）。 */
  var lastSidebarW = -1;
  function detectSidebar() {
    var w = 0;
    var cands = document.querySelectorAll('aside, [class*="sidebar" i], [class*="side-nav" i], [class*="sidenav" i], nav');
    for (var i = 0; i < cands.length; i++) {
      var r = cands[i].getBoundingClientRect();
      if (r.left <= 1 && r.top <= 1 && r.height > window.innerHeight * 0.8
          && r.width >= 100 && r.width <= 500 && r.width > w) {
        w = Math.round(r.width);
      }
    }
    if (w !== lastSidebarW) {
      lastSidebarW = w;
      post({ type: "dsh-desktop-sidebar", width: w });
    }
  }
  function start() {
    detectTheme(); detectSidebar();
    var t = document.documentElement;
    new MutationObserver(function(){ setTimeout(function(){ detectTheme(); detectSidebar(); }, 50); })
      .observe(t, { attributes: true, attributeFilter: ["class","data-theme","style"], childList: true, subtree: true });
    var mq = window.matchMedia("(prefers-color-scheme: dark)");
    try { mq.addEventListener("change", function(){ setTimeout(detectTheme, 50); }); } catch(e) {}
    window.addEventListener("resize", function(){ setTimeout(detectSidebar, 80); });
    window.addEventListener("load", function(){ setTimeout(function(){ detectTheme(); detectSidebar(); }, 150); });
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
  else start();
})();
</script>`
	if i := strings.Index(body, "</head>"); i >= 0 {
		return body[:i] + script + body[i:]
	}
	return script + body
}

// decodeMaybeCompressed 读取并解压响应体（gzip/deflate），返回 HTML 字符串，
// 同时重置 resp.Body 为未压缩.reader（ModifyResponse 后由 ReverseProxy 复制）。
func decodeMaybeCompressed(resp *http.Response) (string, error) {
	var raw bytes.Buffer
	if _, err := io.Copy(&raw, resp.Body); err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	data := raw.Bytes()
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		out, err := io.ReadAll(zr)
		if err != nil {
			return "", err
		}
		data = out
	case "deflate":
		fr := flate.NewReader(bytes.NewReader(data))
		out, err := io.ReadAll(fr)
		if err != nil {
			return "", err
		}
		data = out
	}
	return string(data), nil
}
