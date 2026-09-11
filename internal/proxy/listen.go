package proxy

import (
	"errors"
	"net"
)

var errNoCookie = errors.New("DSH 未返回会话 cookie（token 兑换失败）")

// newLoopbackListener 在 127.0.0.1 随机端口建立监听。
func newLoopbackListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}
