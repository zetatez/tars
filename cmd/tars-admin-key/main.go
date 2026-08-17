// tars-admin-key 是一个独立小工具：给定服务器地址（ip:port），
// 调用无鉴权接口 GET /api/v1/machine-id 获取机器指纹，再按与 tars 服务端
// 一致的规则派生该服务器的 admin key。
//
// 用法：
//
//	tars-admin-key -server <ip:port|url> [-insecure]
//
// 注意：admin key 由公开的 machine-id 直接派生，对能访问该接口者完全可重算，
// 仅适合内网/低安全场景。
package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"tars/internal/auth"
)

func main() {
	var (
		server   = flag.String("server", "", "目标服务器地址，如 192.168.1.10:8899 或 http://host:8899（必填）")
		insecure = flag.Bool("insecure", false, "跳过 HTTPS 证书校验（自签证书/内网时使用）")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "用法: %s -server <ip:port|url> [-insecure]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if *server == "" {
		fmt.Fprintln(os.Stderr, "error: -server 必填，例如 -server 192.168.1.10:8899")
		os.Exit(1)
	}

	base := *server
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	base = strings.TrimRight(base, "/")

	client := &http.Client{Timeout: 10 * time.Second}
	if *insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}

	resp, err := client.Get(base + "/api/v1/machine-id")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: 获取 %s 的 machine-id 失败: %v\n", base, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "error: %s 返回 %d: %s\n", base+"/api/v1/machine-id", resp.StatusCode, strings.TrimSpace(string(body)))
		os.Exit(1)
	}
	var m struct {
		MachineID string `json:"machine_id"`
	}
	if err := json.Unmarshal(body, &m); err != nil || m.MachineID == "" {
		fmt.Fprintf(os.Stderr, "error: 响应格式异常（非 tars 服务？）: %s\n", strings.TrimSpace(string(body)))
		os.Exit(1)
	}

	key := auth.DeriveAdminKey(m.MachineID)
	idx := strings.IndexByte(key, '_')
	fmt.Printf("server      : %s\n", base)
	fmt.Printf("machine_id  : %s\n", m.MachineID)
	fmt.Printf("key_id      : %s\n", key[:idx])
	fmt.Printf("admin_key   : %s\n", key)
	fmt.Println()
	fmt.Println("规则：admin_key = <key_id>_<hex(SHA-256^4096(machine_id))>；")
	fmt.Println("secret 经域分离 HMAC + 4096 轮 SHA-256 迭代扩展，不同服务器 machine-id 不同 → key 不同。")
}
