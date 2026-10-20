package bootstrap

import (
	"reflect"
	"runtime"
	"testing"
)

// npm 的依赖安装脚本（koffi 的 install 脚本调 node）按 PATH 找可执行文件；
// 捆绑 node 不前置进子进程 PATH，在没有全局 node 的机器上更新必失败。
func TestPrependPath(t *testing.T) {
	sep := ";"
	if runtime.GOOS != "windows" {
		sep = ":"
	}

	env := []string{"PATH=" + "/old", "KEY=value"}
	got := prependPath(env, "/node")
	want := []string{"PATH=" + "/node" + sep + "/old", "KEY=value"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("prependPath = %v, want %v", got, want)
	}

	// Windows 环境变量名大小写不敏感，PATH 可能写成 Path。
	env = []string{"Path=" + "/old"}
	got = prependPath(env, "/node")
	want = []string{"PATH=" + "/node" + sep + "/old"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("prependPath(Path=) = %v, want %v", got, want)
	}

	// 无 PATH 时追加一条。
	got = prependPath([]string{"KEY=value"}, "/node")
	want = []string{"KEY=value", "PATH=/node"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("prependPath(无 PATH) = %v, want %v", got, want)
	}
}

// lineWriter 必须流式拆行：npm install 的一两分钟里逐行回调，
// 前端才有实时进度（分片含 \r\n、半行跨越多次 Write）。
func TestLineWriter(t *testing.T) {
	var got []string
	w := &lineWriter{onLine: func(line string) { got = append(got, line) }}

	w.Write([]byte("npm info ok\r\nadd"))
	w.Write([]byte("ed 520 pack"))
	w.Write([]byte("ages\nnpm war"))
	w.Write([]byte("n cleanup ..."))

	// 只有完整行被回调；结尾无换行的半行留在缓冲等下一次 Write。
	want := []string{"npm info ok", "added 520 packages"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lineWriter 回调 = %q, want %q", got, want)
	}
	if string(w.buf) != "npm warn cleanup ..." {
		t.Errorf("残留缓冲 = %q", w.buf)
	}
}
