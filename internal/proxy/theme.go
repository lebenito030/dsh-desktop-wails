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
// 同时按「侧栏右侧、贴顶通高的最外层列」为壳的顶部拖动条让位 32px
//（**纯结构探测，不依赖 DSH 的类名**——发布产物走 CSS Modules，类名会被
// hash，匹配类名注定不可靠）：文档流列补 padding-top；绝对定位的右栏
// 面板（top:0 钉在列的 padding-box 上，padding 推不动）改它自己的内联 top。
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
     两类候选要区别对待：
     - 文档流候选（DSH 中列）：补 padding-top；
     - 绝对定位候选（DSH 右栏面板，position:absolute + top:0 钉在列的
       padding-box 上）：祖先的 padding 推不动它，必须改它自己的内联 top
       （它自带 bottom:0，top 下移即整体收缩）。
     必须幂等：React 重渲染会抹掉内联样式，观察器会在下个节拍补回来。
     只保留最外层候选（去掉互为祖先的），避免嵌套容器被叠加两次。 */
  var SHELL_TOP = ` + strconv.Itoa(shellTopBarPx) + `;
  var padded = [], shifted = [];
  function detectColumns(sidebarW) {
    if (!sidebarW || sidebarW <= 0) return;
    var all = document.querySelectorAll("body *");
    var cands = [];
    for (var i = 0; i < all.length; i++) {
      var el = all[i];
      var r = el.getBoundingClientRect();
      if (r.width < 60 || r.width >= window.innerWidth - 40) continue;
      if (r.left < sidebarW - 1) continue;
      /* 起点允许到让位区底部：已让位的候选（top=32）要继续命中，
         否则 React 一重渲染就会失去让位。 */
      if (r.top > SHELL_TOP) continue;
      /* 接近通高即可：右侧栏底部可能有内边距/状态区，够不到窗口最底沿。 */
      if (r.bottom < window.innerHeight - 60) continue;
      var pos = getComputedStyle(el).position;
      /* fixed 是弹层（含右栏的全屏态），一律不动。 */
      if (pos === "fixed") continue;
      /* absolute 只认贴着右缘的：右栏面板 right:0 钉在窗口右缘；
         窗口中部的浮动面板不是列，不能动。用 clientWidth 而非
         innerWidth：后者含页面滚动条宽度，会把贴右缘的面板误判掉。 */
      if (pos === "absolute" && r.right < document.documentElement.clientWidth - 1) continue;
      cands.push(el);
    }
    var tops = [];
    for (var j = 0; j < cands.length; j++) {
      var posJ = getComputedStyle(cands[j]).position;
      var outermost = true;
      for (var k = 0; k < cands.length; k++) {
        if (k === j || !cands[k].contains(cands[j])) continue;
        if (posJ !== "absolute") { outermost = false; break; }
        /* j 是 absolute：文档流祖先的 padding 推不动它，不能因此剥离；
           但 absolute 祖先靠自己的 top 让位，会把它一起带走，仍要剥离。 */
        if (getComputedStyle(cands[k]).position === "absolute") { outermost = false; break; }
      }
      if (outermost) tops.push(cands[j]);
    }
    for (var a = 0; a < tops.length; a++) {
      var el = tops[a];
      if (getComputedStyle(el).position === "absolute") {
        /* 右栏面板：改自己的 top（自带 bottom:0，改 top 即收缩）。
           只在它还顶着让位带上沿时移动，避免把已经移过的又推一遍。 */
        if (el.style.top !== SHELL_TOP + "px") {
          var cur = parseFloat(el.style.top);
          if (isNaN(cur) || cur <= SHELL_TOP) el.style.top = SHELL_TOP + "px";
        }
        if (shifted.indexOf(el) === -1) shifted.push(el);
      } else {
        if (el.style.paddingTop !== SHELL_TOP + "px") el.style.paddingTop = SHELL_TOP + "px";
        if (padded.indexOf(el) === -1) padded.push(el);
      }
    }
    for (var b = padded.length - 1; b >= 0; b--) {
      if (tops.indexOf(padded[b]) === -1) {
        padded[b].style.paddingTop = "";
        padded.splice(b, 1);
      }
    }
    for (var c = shifted.length - 1; c >= 0; c--) {
      if (tops.indexOf(shifted[c]) === -1) {
        shifted[c].style.top = "";
        shifted.splice(c, 1);
      }
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
