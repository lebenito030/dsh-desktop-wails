// Package proxy 提供带 cookie 托管的 DSH 反向代理。
//
// 背景：DSH 的浏览器会话用 SameSite=Strict 的登录 cookie。壳页面的
// iframe 与 DSH 服务必然跨站（wails.localhost → 127.0.0.1:port），
// 浏览器在跨站 iframe 里既不存也不发该 cookie，token 兑换必然失败
// （表现为 "dsh web authentication required"）。
//
// 解法：cookie 由代理持有，不经浏览器——
//  1. ExchangeToken 用就绪 URL（含 ?token=）请求一次，截获 Set-Cookie；
//  2. 代理对每个请求附上该 cookie，并把 Origin/Referer 改写为 DSH 的
//     authority（通过 DSH 的 Host/Origin 信任围栏）；
//  3. iframe 指向代理地址；WebSocket 由 httputil.ReverseProxy 原生升级转发。
package proxy

import (
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
)

// Proxy 是一个持有 DSH 会话 cookie 的反代。
type Proxy struct {
	mu      sync.RWMutex
	target  *url.URL // DSH 服务根（http://127.0.0.1:port）
	cookies []*http.Cookie
	rp      *httputil.ReverseProxy
	srv     *http.Server
	Addr    string // 监听地址（Start 后可读）
}

// New 创建未启动的代理。
func New() *Proxy {
	return &Proxy{}
}

// SetTarget 更新上游与 cookie（每次 DSH 重启后端口与 token 都会变）。
func (p *Proxy) SetTarget(targetURL string, cookies []*http.Cookie) error {
	tu, err := url.Parse(targetURL)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.target = tu
	p.cookies = cookies
	p.mu.Unlock()
	if p.rp == nil {
		p.rp = httputil.NewSingleHostReverseProxy(tu)
		rp := p.rp
		original := rp.Director
		rp.Director = func(req *http.Request) {
			original(req)
			p.direct(req)
		}
		// 向 DSH 的 HTML 注入主题上报脚本：iframe 跨源，壳无法读取页面
		// 配色；由页面自己检测背景亮度并 postMessage 出来。
		rp.ModifyResponse = func(resp *http.Response) error {
			ct := resp.Header.Get("Content-Type")
			if !strings.Contains(ct, "text/html") {
				return nil
			}
			body, err := decodeMaybeCompressed(resp)
			if err != nil {
				return nil // 注入失败不阻断页面
			}
			if strings.Contains(body, themeProbeMarker) {
				return nil // 已注入
			}
			injected := injectThemeProbe(body)
			resp.Body = io.NopCloser(strings.NewReader(injected))
			resp.Header.Del("Content-Length")
			resp.Header.Del("Content-Encoding")
			return nil
		}
		rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy: %s %s: %v", r.Method, r.URL.Path, err)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("DSH 服务暂不可用（可能正在重启）"))
		}
	} else {
		p.rp.Director = func(req *http.Request) {
			rewriteTarget(req, tu)
			p.direct(req)
		}
	}
	return nil
}

// rewriteTarget 是 NewSingleHostReverseProxy 的 Director 逻辑（供更新上游时复用）。
func rewriteTarget(req *http.Request, target *url.URL) {
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	if target.Path == "" {
		req.URL.Path = singleJoiningSlash(target.Path, req.URL.Path)
	}
	req.Host = target.Host
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// direct 给请求附 cookie 并改写 Origin/Referer 以通过 DSH 信任围栏。
func (p *Proxy) direct(req *http.Request) {
	p.mu.RLock()
	target := p.target
	cookies := p.cookies
	p.mu.RUnlock()
	if target == nil {
		return
	}
	req.Host = target.Host
	// 要求上游返回未压缩内容，便于 ModifyResponse 注入主题脚本。
	req.Header.Set("Accept-Encoding", "identity")
	if len(cookies) > 0 {
		req.Header.Del("Cookie")
		for _, c := range cookies {
			req.AddCookie(c)
		}
	}
	// DSH 围栏：Origin 若存在必须等于 Host authority。浏览器会带
	// Origin: http://127.0.0.1:<proxyPort>；改写为上游 authority。
	if origin := req.Header.Get("Origin"); origin != "" {
		req.Header.Set("Origin", target.Scheme+"://"+target.Host)
	}
	if referer := req.Header.Get("Referer"); referer != "" {
		req.Header.Set("Referer", target.Scheme+"://"+target.Host+"/")
	}
}

// ExchangeToken 用含 token 的就绪 URL 换取会话 cookie。
// DSH 对 ?token= 请求回 303 + Set-Cookie；这里禁跟随重定向，只收 cookie。
func ExchangeToken(readyURL string) ([]*http.Cookie, error) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(readyURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		return nil, errNoCookie
	}
	return cookies, nil
}

// Start 在 127.0.0.1 的随机端口上开始服务。阻塞前返回监听地址。
func (p *Proxy) Start() error {
	ln, err := newLoopbackListener()
	if err != nil {
		return err
	}
	p.Addr = "http://" + ln.Addr().String()
	p.srv = &http.Server{Handler: p}
	go func() {
		_ = p.srv.Serve(ln)
	}()
	return nil
}

// Stop 关闭代理服务。
func (p *Proxy) Stop() error {
	if p.srv == nil {
		return nil
	}
	return p.srv.Close()
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	rp := p.rp
	p.mu.RUnlock()
	if rp == nil {
		http.Error(w, "DSH 尚未就绪", http.StatusServiceUnavailable)
		return
	}
	rp.ServeHTTP(w, r)
}
