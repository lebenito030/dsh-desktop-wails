package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 代理的核心承诺：浏览器发来的请求，到达 DSH 时必须带上 cookie，
// 且 Origin/Referer/Host 都被改写成 DSH 自己的 authority（通过它的信任围栏）。
// 这是整个「cookie 不经过浏览器」方案的地基。
func TestProxyAttachesCookieAndRewrites(t *testing.T) {
	var gotPath, gotHost, gotCookie, gotOrigin, gotReferer, gotAcceptEnc string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		gotHost = r.Host
		gotCookie = r.Header.Get("Cookie")
		gotOrigin = r.Header.Get("Origin")
		gotReferer = r.Header.Get("Referer")
		gotAcceptEnc = r.Header.Get("Accept-Encoding")
		_, _ = io.WriteString(w, "upstream-body")
	}))
	defer upstream.Close()

	p := New()
	if err := p.SetTarget(upstream.URL, []*http.Cookie{
		{Name: "dsh_session", Value: "s3cret"},
		{Name: "other", Value: "x"},
	}); err != nil {
		t.Fatalf("SetTarget: %v", err)
	}

	proxySrv := httptest.NewServer(p)
	defer proxySrv.Close()

	// 模拟浏览器：Origin/Referer 指向代理（跨站），带一个浏览器自作主张的 cookie。
	req, _ := http.NewRequest(http.MethodGet, proxySrv.URL+"/assets/app.js?v=2", nil)
	req.Header.Set("Origin", proxySrv.URL)
	req.Header.Set("Referer", proxySrv.URL+"/")
	req.Header.Set("Cookie", "stale-browser-cookie=1")
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求代理: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if string(body) != "upstream-body" {
		t.Errorf("body = %q", body)
	}
	if gotPath != "/assets/app.js?v=2" {
		t.Errorf("路径未保留: %q", gotPath)
	}
	if gotCookie != "dsh_session=s3cret; other=x" {
		t.Errorf("Cookie = %q（应替换为代理持有的会话 cookie）", gotCookie)
	}
	if gotOrigin != upstream.URL {
		t.Errorf("Origin = %q, want %q", gotOrigin, upstream.URL)
	}
	if gotReferer != upstream.URL+"/" {
		t.Errorf("Referer = %q, want %q", gotReferer, upstream.URL+"/")
	}
	if gotAcceptEnc != "identity" {
		t.Errorf("Accept-Encoding = %q, want identity（压缩会挡住主题注入）", gotAcceptEnc)
	}
	if !strings.HasSuffix(gotHost, strings.TrimPrefix(upstream.URL, "http://")) {
		t.Errorf("Host = %q, want 上游 authority", gotHost)
	}
}

// 未 SetTarget 时不能 500，要给用户一句能看懂的提示。
func TestProxyWithoutTargetReturns503(t *testing.T) {
	srv := httptest.NewServer(New())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

// DSH 重启后端口和 token 都会变：SetTarget 第二次调用必须真的切到新上游。
func TestProxyFollowsTargetUpdate(t *testing.T) {
	hit := ""
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = "first"
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = "second"
	}))
	defer second.Close()

	p := New()
	if err := p.SetTarget(first.URL, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.SetTarget(second.URL, []*http.Cookie{{Name: "k", Value: "v"}}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(p)
	defer srv.Close()
	if _, err := http.Get(srv.URL); err != nil {
		t.Fatal(err)
	}
	if hit != "second" {
		t.Errorf("请求落在了 %q，应切到新上游", hit)
	}
}

// 主题探针只注入 HTML，且不能重复注入。
func TestModifyResponseInjectsOnce(t *testing.T) {
	serve := func(body string, ctype string) *httptest.Server {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ctype)
			_, _ = io.WriteString(w, body)
		}))
		return upstream
	}

	// HTML：注入一次；再请求一次也不能叠加。
	up := serve("<html><head></head><body>hi</body></html>", "text/html; charset=utf-8")
	p := New()
	if err := p.SetTarget(up.URL, nil); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(p)
	for i := 0; i < 2; i++ {
		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), themeProbeMarker) {
			t.Errorf("第 %d 次：HTML 未注入主题探针", i+1)
		}
		if n := strings.Count(string(body), themeProbeMarker); n != 1 {
			t.Errorf("第 %d 次：探针出现 %d 次，应恰好 1 次", i+1, n)
		}
	}
	up.Close()

	// 非 HTML：原样透传。
	up2 := serve(`{"ok":true}`, "application/json")
	defer up2.Close()
	p2 := New()
	if err := p2.SetTarget(up2.URL, nil); err != nil {
		t.Fatal(err)
	}
	srv2 := httptest.NewServer(p2)
	defer srv2.Close()
	resp, err := http.Get(srv2.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), themeProbeMarker) {
		t.Errorf("JSON 响应不应被注入: %s", body)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("JSON 响应被改动: %s", body)
	}
}

// ExchangeToken 只收 cookie，不跟随 303。
func TestExchangeTokenCollectsCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "abc" {
			t.Errorf("token 未随请求发出: %q", r.URL.RawQuery)
		}
		http.SetCookie(w, &http.Cookie{Name: "dsh_session", Value: "s3cret", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "dsh_csrf", Value: "c", Path: "/"})
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer srv.Close()

	cookies, err := ExchangeToken(srv.URL + "/?token=abc")
	if err != nil {
		t.Fatalf("ExchangeToken: %v", err)
	}
	if len(cookies) != 2 {
		t.Fatalf("cookies = %d, want 2", len(cookies))
	}
	if cookies[0].Name != "dsh_session" || cookies[0].Value != "s3cret" {
		t.Errorf("第一枚 cookie = %+v", cookies[0])
	}
}

func TestExchangeTokenWithoutCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if _, err := ExchangeToken(srv.URL + "/?token=abc"); err != errNoCookie {
		t.Fatalf("err = %v, want errNoCookie", err)
	}
}
