//go:build !windows

package update

import "net/http"

// 非 Windows 没有统一的「系统代理」来源（gsettings / macOS scutil 各成一派），
// 且环境变量语义已由直连路径的 ProxyFromEnvironment 覆盖；这里不提供第二跳，
// 路径序列退化为 直连 → 直连重试。变量而非函数：与 Windows 侧保持同形，测试可替换。
var systemProxyClient = func() *http.Client { return nil }
