// Package update 检查 @deepseek-ai/dsh 的 npm registry 最新版并与本地安装对比。
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dsh-desktop-wails/internal/dsh"
)

// Checker 查询 registry 并对比本地版本。
type Checker struct {
	// Registry 形如 https://registry.npmjs.org。
	Registry string
	// Package 是包名（@deepseek-ai/dsh）。
	Package string
}

// Result 是一次检查的产物。
type Result struct {
	// LocalVersion 是本地已装版本；未安装为空串（此时 Available 恒 false，
	// 首装由 bootstrap 负责，不走更新流程）。
	LocalVersion string
	// LatestVersion 是 registry dist-tag latest 指向的版本。
	LatestVersion string
	// Available 表示 registry 版本比本地新。
	Available bool
}

// namedClient 是一条网络路径：名字只用于注释与测试，便于分辨「直连」「系统代理」。
type namedClient struct {
	name   string
	client *http.Client
}

const (
	// attemptTimeout 是单条路径的独立超时。全部路径挂掉最坏 3 跳 + 2 次间隔，
	// 但检查是异步的（启动后台查 / 手动查走 toast），多等几秒换成功率高，值。
	attemptTimeout = 8 * time.Second
	// retryDelay 是两跳之间的间隔，给被重置的连接一点恢复时间。
	retryDelay = time.Second
)

// attemptClients 返回按序尝试的网络路径。包级变量是为了让测试替换路径编排。
// 直连（含 HTTPS_PROXY 环境变量语义，由 ProxyFromEnvironment 提供）→
// Windows 系统代理 → 直连重试（防代理本身半死）。第二跳的存在理由：
// Go 的 net/http 只认环境变量，不读 WinINET 系统代理，Clash/V2Ray 用户
// 「代理明明开着」对壳却不可见；而国内直连 npm registry 间歇被重置是实测常态。
var attemptClients = func() []namedClient {
	clients := []namedClient{{name: "直连", client: http.DefaultClient}}
	if c := systemProxyClient(); c != nil {
		clients = append(clients, namedClient{name: "系统代理", client: c})
	}
	return append(clients, namedClient{name: "直连重试", client: http.DefaultClient})
}

// Latest 拉 registry 的 latest 元数据，仅取 version 字段。
// 传输层失败才换下一跳重试；registry 已应答（如 404）说明换路无济于事，直接上报。
// 全部失败时报最后一跳的原因——各跳失败同为网络类错误，取其一足以指导排障。
func (c Checker) Latest(ctx context.Context) (string, error) {
	url := fmt.Sprintf("%s/%s/latest", strings.TrimRight(c.Registry, "/"), c.Package)
	var lastErr error
	for i, nc := range attemptClients() {
		if i > 0 {
			select {
			case <-time.After(retryDelay):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		ver, err := fetchLatest(ctx, nc.client, url)
		if err == nil {
			return ver, nil
		}
		var se *statusError
		if errors.As(err, &se) {
			return "", err
		}
		lastErr = err
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("查询 registry 失败: %w", lastErr)
}

// fetchLatest 用给定 client 请求一次 latest 元数据。
func fetchLatest(ctx context.Context, client *http.Client, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &statusError{Status: resp.Status}
	}
	var meta struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", fmt.Errorf("解析 registry 响应: %w", err)
	}
	if meta.Version == "" {
		return "", errors.New("registry 响应缺少 version")
	}
	return meta.Version, nil
}

// statusError 表示 registry 已应答但状态码非 200——这种失败换网络路径没有意义。
type statusError struct{ Status string }

func (e *statusError) Error() string { return "registry 返回 " + e.Status }

// Check 组合本地版本与 registry latest，判定是否有更新。
// 本地未安装（空版本）时 Available=false，不提示更新。
func (c Checker) Check(ctx context.Context, paths dsh.Paths) (Result, error) {
	local := paths.LocalVersion()
	latest, err := c.Latest(ctx)
	if err != nil {
		return Result{}, err
	}
	return Result{
		LocalVersion:  local,
		LatestVersion: latest,
		Available:     local != "" && newer(latest, local),
	}, nil
}

// newer 判断 a 是否严格新于 b。npm 语义版本（可能带 -rc.N 预发布），
// 按主.次.补丁数值比较；预发布比同号正式版低。解析失败时退回字符串不等比较。
func newer(a, b string) bool {
	pa, aok := parseSemver(a)
	pb, bok := parseSemver(b)
	if !aok || !bok {
		return a != b
	}
	for i := 0; i < 3; i++ {
		if pa.nums[i] != pb.nums[i] {
			return pa.nums[i] > pb.nums[i]
		}
	}
	// 三段相等：正式版 > 预发布；预发布之间按字符串比较（够用）。
	switch {
	case pa.pre == "" && pb.pre != "":
		return true
	case pa.pre != "" && pb.pre == "":
		return false
	default:
		return pa.pre > pb.pre
	}
}

type semver struct {
	nums [3]int
	pre  string
}

func parseSemver(v string) (semver, bool) {
	s := strings.TrimPrefix(strings.TrimSpace(v), "v")
	main := s
	pre := ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		main, pre = s[:i], s[i+1:]
	}
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var out semver
	for i, p := range parts {
		n := 0
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return semver{}, false
			}
			n = n*10 + int(ch-'0')
		}
		out.nums[i] = n
	}
	out.pre = pre
	return out, true
}
