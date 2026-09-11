package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// themeProbeMarker 标识注入已存在，避免重复注入。
const themeProbeMarker = "dshDesktopThemeProbe"

// shellTopBarPx 是壳顶部拖动条的高度。**必须与前端 style.css 的 --titlebar-h 一致**。
const shellTopBarPx = 32

// injectThemeProbe 把上报脚本插到 </head> 前（无 </head> 时插到页首）。
// 上报两类信息给壳（iframe 父页面）：
//  1. 主题：检测文档背景亮度，供窗口按钮切换配色；
//  2. 侧栏几何：侧栏宽度变化时上报，供壳动态移动顶部拖动条，
//     避免遮挡侧栏折叠按钮。
//
// 同时按「侧栏右侧、贴顶通高的最外层列」给 DSH 的列补 padding-top，
// 为顶部拖动条让位（**纯结构探测，不依赖 DSH 的类名**——发布产物走 CSS
// Modules，类名会被 hash，匹配类名注定不可靠）。
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
      detectColumns(w);
    }
  }
  /* 列让位（纯结构探测，不依赖类名）：给「侧栏右侧、贴顶通高」的最外层列
     补 padding-top，为壳的顶部拖动条让位。
     必须幂等：React 重渲染会抹掉内联样式，观察器会在下个节拍补回来。
     只保留最外层候选（去掉互为祖先的），避免嵌套容器被叠加两次。 */
  var SHELL_TOP = ` + strconv.Itoa(shellTopBarPx) + `;
  var padded = [];
  function detectColumns(sidebarW) {
    if (!sidebarW || sidebarW <= 0) return;
    var all = document.querySelectorAll("body *");
    var cands = [];
    for (var i = 0; i < all.length; i++) {
      var el = all[i];
      var r = el.getBoundingClientRect();
      if (r.width < 40 || r.width >= window.innerWidth - 40) continue;
      if (r.left < sidebarW - 1) continue;
      if (r.top > 1 || r.bottom < window.innerHeight - 1) continue;
      var cs = getComputedStyle(el);
      if (cs.position === "fixed" || cs.position === "absolute") continue;
      cands.push(el);
    }
    var tops = [];
    for (var j = 0; j < cands.length; j++) {
      var outermost = true;
      for (var k = 0; k < cands.length; k++) {
        if (k !== j && cands[k].contains(cands[j])) { outermost = false; break; }
      }
      if (outermost) tops.push(cands[j]);
    }
    for (var a = 0; a < tops.length; a++) {
      if (tops[a].style.paddingTop !== SHELL_TOP + "px") tops[a].style.paddingTop = SHELL_TOP + "px";
    }
    for (var b = padded.length - 1; b >= 0; b--) {
      if (tops.indexOf(padded[b]) === -1) {
        padded[b].style.paddingTop = "";
        padded.splice(b, 1);
      }
    }
    for (var c = 0; c < tops.length; c++) {
      if (padded.indexOf(tops[c]) === -1) padded.push(tops[c]);
    }
  }
  function start() {
    detectTheme(); detectSidebar();
    var t = document.documentElement;
    new MutationObserver(function(){ setTimeout(function(){ detectTheme(); detectSidebar(); detectColumns(lastSidebarW); }, 50); })
      .observe(t, { attributes: true, attributeFilter: ["class","data-theme","style"], childList: true, subtree: true });
    var mq = window.matchMedia("(prefers-color-scheme: dark)");
    try { mq.addEventListener("change", function(){ setTimeout(detectTheme, 50); }); } catch(e) {}
    window.addEventListener("resize", function(){ setTimeout(function(){ detectSidebar(); detectColumns(lastSidebarW); }, 80); });
    window.addEventListener("load", function(){ setTimeout(function(){ detectTheme(); detectSidebar(); detectColumns(lastSidebarW); }, 150); });
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
