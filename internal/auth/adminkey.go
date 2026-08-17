package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
)

// adminKeyPrefix 是派生 admin key_id 的前缀，用于标识其来源。
const adminKeyPrefix = "tars-admin"

// adminKeyDomain 派生域分离串：绑定算法版本，避免与其它用途的哈希串混淆。
const adminKeyDomain = "tars:admin-key:v2"

// deriveIterations 是 secret 的迭代扩展轮数（key stretching，增加暴力破解成本）。
const deriveIterations = 4096

// MachineID 返回该主机的稳定唯一标识。
// 优先 /etc/machine-id（systemd 生成的每机随机 UUID，正常部署稳定且唯一）；
// 缺失时回退 boot_id 与 hostname。
func MachineID() string {
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	if b, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}

// DeriveAdminKey 由机器指纹派生 admin key（无需口令），格式 `<key_id>_<64 hex>`。
//
// 规则：
//   - t0 = HMAC-SHA256(domain="tars:admin-key:v2", machineID)          （域分离首轮）
//   - ti = SHA-256(t[i-1] || machineID || little-endian(i))，i = 1..4096（迭代扩展）
//   - secret = hex(t4096)
//   - key_id = "tars-admin-" + hex(HMAC-SHA256(domain, "keyid:"+machineID))[:12]
//
// 不同服务器 machine-id 不同 → admin key 不同。迭代扩展增加攻击成本。
// 安全注意：machine-id 经无鉴权接口 `GET /api/v1/machine-id` 暴露，
// 因此本派生对能访问该接口者仍可重算，适合内网/低安全场景。
func DeriveAdminKey(machineID string) string {
	// 域分离首轮：HMAC-SHA256(domain, machineID)
	h := hmac.New(sha256.New, []byte(adminKeyDomain))
	h.Write([]byte(machineID))
	t := h.Sum(nil)

	// 迭代扩展：t = SHA-256(t || machineID || i)
	var cnt [4]byte
	for i := 1; i <= deriveIterations; i++ {
		binary.LittleEndian.PutUint32(cnt[:], uint32(i))
		d := sha256.New()
		d.Write(t)
		d.Write([]byte(machineID))
		d.Write(cnt[:])
		t = d.Sum(nil)
	}
	secret := hex.EncodeToString(t) // 64 hex

	idMAC := hmac.New(sha256.New, []byte(adminKeyDomain))
	idMAC.Write([]byte("keyid:" + machineID))
	keyID := adminKeyPrefix + "-" + hex.EncodeToString(idMAC.Sum(nil))[:12]

	return keyID + "_" + secret
}

// AdminKeyFromConfig 派生本机 admin key：基于本机 machine-id，无需任何配置。
func AdminKeyFromConfig() (string, error) {
	return DeriveAdminKey(MachineID()), nil
}
