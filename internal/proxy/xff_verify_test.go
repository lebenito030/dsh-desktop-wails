package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 回归守卫：反向代理不得给上游补 X-Forwarded-For / CF-Connecting-IP。
// @linxin666/dsh-usage、dsh-update 等插件共用 loopback 信任围栏
// isLoopbackRequest()，见到这两个头就把请求判成「经过代理」并回 403；
// 这里用 Rewrite（而非 Director）就是为了不触发 Go stdlib 的自动 XFF 注入。
func TestFixedProxyAddsNoForwardingHeaders(t *testing.T) {
	var gotXFF, gotCF, gotHost string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXFF = r.Header.Get("X-Forwarded-For")
		gotCF = r.Header.Get("CF-Connecting-IP")
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	p := New()
	if err := p.SetTarget(up.URL, nil); err != nil {
		t.Fatalf("SetTarget: %v", err)
	}
	front := httptest.NewServer(p)
	defer front.Close()

	resp, err := http.Get(front.URL + "/api/dsh-usage/overview")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if gotXFF != "" {
		t.Fatalf("上游看到了 X-Forwarded-For=%q（修复回退）", gotXFF)
	}
	if gotCF != "" {
		t.Fatalf("上游看到了 CF-Connecting-IP=%q", gotCF)
	}
	t.Logf("OK: status=%d upstreamHost=%q xff=%q cf=%q", resp.StatusCode, gotHost, gotXFF, gotCF)
}
