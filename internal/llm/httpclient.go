package llm

import (
	"net"
	"net/http"
	"time"
)

// 连接阶段超时：provider 挂起（DNS/建连/握手/等待响应头）时快速失败，以便 pool failover。
// 测试可覆盖为更短值。
var (
	dialTimeout           = 10 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	responseHeaderTimeout = 60 * time.Second
)

// newHTTPClient 构造带连接/响应头超时的 client。
// 不设整体 Timeout：流式响应可能持续较久，只约束"建立连接与拿到响应头"阶段，
// 之后由各 provider 的 stream idle timeout 接管。
func newHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{Transport: transport}
}
