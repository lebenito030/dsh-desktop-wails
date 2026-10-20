//go:build windows

package update

import (
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// systemProxyClient 读取 WinINET 系统代理（设置 → 网络 → 代理那个开关，
// Clash/V2Ray 的「系统代理」写的就是它）并返回走该代理的 client。
// 没开代理、PAC 模式（AutoConfigURL）或解析失败一律返回 nil——回退路径缺席，
// 行为退化为原来的直连重试，无害。变量而非函数：测试里替换成桩。
var systemProxyClient = func() *http.Client {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enable == 0 {
		return nil
	}
	ps, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return nil
	}
	u := parseProxyServer(ps)
	if u == nil {
		return nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(u)
	return &http.Client{Transport: tr}
}

// parseProxyServer 解析 WinINET ProxyServer 的三种写法：
//   - "127.0.0.1:7897"（单一地址，各协议共用）
//   - "http=h:1;https=h:2;ftp=h:3"（按协议，https 优先、http 次之）
//   - "socks=127.0.0.1:7891"（SOCKS 代理，映射成 socks5://）
//
// ProxyOverride（例外列表）不处理：registry 域名几乎不会被用户加进例外。
func parseProxyServer(ps string) *url.URL {
	ps = strings.TrimSpace(ps)
	if ps == "" {
		return nil
	}
	if !strings.Contains(ps, "=") {
		return proxyURL("http", ps)
	}
	var addr, scheme string
	for _, part := range strings.Split(ps, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || v == "" {
			continue
		}
		switch strings.ToLower(k) {
		case "https":
			addr, scheme = v, "http" // https 流量走的是 HTTP 代理的 CONNECT 隧道
		case "http":
			if addr == "" {
				addr, scheme = v, "http"
			}
		case "socks":
			if addr == "" {
				addr, scheme = v, "socks5"
			}
		}
	}
	return proxyURL(scheme, addr)
}

// proxyURL 把 "host[:port]" 变成代理 URL；自带 scheme 的地址直接解析，
// 硬拼会产出 http://http://… 这种坏 URL。解析不出 host 或 scheme 不在
// http/https/socks5 之列的，视为坏配置返回 nil。
func proxyURL(scheme, addr string) *url.URL {
	if addr == "" {
		return nil
	}
	raw := scheme + "://" + addr
	if i := strings.Index(addr, "://"); i >= 0 {
		raw = addr
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil
	}
	switch u.Scheme {
	case "http", "https", "socks5":
		return u
	default:
		return nil
	}
}
