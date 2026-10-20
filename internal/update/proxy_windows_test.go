//go:build windows

package update

import "testing"

// WinINET ProxyServer 的三种写法都要认；解析不出 host 的坏配置返回 nil，
// 让回退路径缺席，而不是带崩整个检查。
func TestParseProxyServer(t *testing.T) {
	cases := []struct {
		in   string
		want string // 空串 = 期望 nil
	}{
		{"127.0.0.1:7897", "http://127.0.0.1:7897"},
		{"proxy.lan", "http://proxy.lan"}, // 无端口也合法
		{"ftp=1.1.1.1:21;https=10.0.0.2:8443;http=10.0.0.1:8080", "http://10.0.0.2:8443"},
		{"http=10.0.0.1:8080", "http://10.0.0.1:8080"},
		{"http=http://10.0.0.1:8080", "http://10.0.0.1:8080"}, // 有的安装器值自带 scheme
		{"socks=127.0.0.1:7891", "socks5://127.0.0.1:7891"},
		{"", ""},
		{"https=", ""}, // 只有空条目
		{"; ;", ""},    // 全是碎片
	}
	for _, tc := range cases {
		got := parseProxyServer(tc.in)
		if tc.want == "" {
			if got != nil {
				t.Errorf("parseProxyServer(%q) = %v, want nil", tc.in, got)
			}
			continue
		}
		if got == nil || got.String() != tc.want {
			t.Errorf("parseProxyServer(%q) = %v, want %s", tc.in, got, tc.want)
		}
	}
}
