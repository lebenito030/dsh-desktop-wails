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
  /* 侧栏探测：DSH 的 AppFrame 把 computeColumns() 的结果直接写在
     frame 容器的内联 style.gridTemplateColumns 上（第一列即侧栏
     轨道，形如 "280px minmax(0, 1fr) 0px"）。读它最可靠：展开
     264~420、收起 56（ui-layout columns.ts 常量）。 */
  var lastSidebarW = -1;
  function detectSidebar() {
    var w = 0;
    var all = document.querySelectorAll('[style]');
    for (var i = 0; i < all.length; i++) {
      var cols = all[i].style && all[i].style.gridTemplateColumns;
      if (!cols) continue;
      var m = cols.match(/^\s*(\d+(?:\.\d+)?)px/);
      if (m && parseFloat(m[1]) > 0 && parseFloat(m[1]) < window.innerWidth) {
        w = Math.round(parseFloat(m[1]));
        break;
      }
    }
    if (w === 0) {
      // 兜底：贴左缘、通高的第一个非全宽子列实测宽度。
      var bodyKids = document.querySelectorAll('body *');
      for (var j = 0; j < bodyKids.length; j++) {
        var r = bodyKids[j].getBoundingClientRect();
        if (r.left <= 1 && r.top <= 1 && r.bottom >= window.innerHeight - 1
            && r.width >= 40 && r.width < window.innerWidth - 40) {
          w = Math.round(r.width);
          break;
        }
      }
    }
    if (w !== lastSidebarW && w > 0) {
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
