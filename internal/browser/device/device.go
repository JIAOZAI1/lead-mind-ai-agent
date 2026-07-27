// Package device 管理浏览器执行端的设备身份：一次性配对码的签发与兑换、
// 长期 device_token 的签发与校验、以及设备列表/吊销。
//
// 不在插件里做账号密码登录——插件是钓鱼重灾区，且要处理验证码/2FA/SSO。
// 改为「用户在已登录的 web 端生成一次性配对码 → 在插件里输入 → 换取长期
// device_token」（插件 DESIGN.md §7.1）。
package device

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"
)

// Device 是一台已配对的浏览器设备。
//
// 它绑定到具体的 (tenant_code, user_id)：指令下发时据此找到"该发给谁的
// 浏览器"，而不是让任意租户都能驱动任意设备。
type Device struct {
	ID          string
	UserID      string
	Name        string
	Fingerprint string
	// TokenHash 是 device_token 的 SHA-256，明文只在配对响应里出现一次。
	// 落库存哈希而非明文：库被读走时攻击者拿不到可直接使用的凭证，这与
	// 密码存储是同一个道理——区别只在于 token 是高熵随机值，不需要 bcrypt
	// 这类慢哈希来抵御离线爆破。
	TokenHash  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

// Revoked 判断设备是否已被吊销。
func (d Device) Revoked() bool { return d.RevokedAt != nil }

// Expired 判断设备凭证是否已过期。
func (d Device) Expired(now time.Time) bool { return now.After(d.ExpiresAt) }

// PairingCode 是一次性配对码在服务端的记录。
type PairingCode struct {
	Code       string
	TenantCode string
	UserID     string
	ExpiresAt  time.Time
}

// CodeStore 保存尚未兑换的配对码。
//
// 实现放在 Redis：配对码天然带 TTL 且必须一次性消费，正是 Redis SET NX +
// GETDEL 的场景，用 MySQL 反而要自己做定时清理和事务化消费。
type CodeStore interface {
	// Issue 保存一个新的配对码，ttl 后自动失效。
	Issue(ctx context.Context, code PairingCode, ttl time.Duration) error

	// Redeem 原子地取出并删除一个配对码。
	//
	// **原子性是安全要求而不是性能优化**：如果先读后删，两个并发请求可能
	// 同时读到同一个码，各自换走一个 device_token，一次性语义就破了。
	// 找不到（不存在或已被兑换）时返回 (PairingCode{}, false, nil)。
	Redeem(ctx context.Context, tenantCode, code string) (PairingCode, bool, error)
}

// Store 持久化保存已配对的设备。实现放在租户自己的 MySQL 库中。
type Store interface {
	// Create 登记一台新配对的设备。
	Create(ctx context.Context, tenantCode string, d Device) error

	// FindByTokenHash 按 token 哈希查找设备；不存在时返回
	// (Device{}, false, nil)。
	FindByTokenHash(ctx context.Context, tenantCode, tokenHash string) (Device, bool, error)

	// TouchLastSeen 记录设备最近一次连接成功的时间。
	TouchLastSeen(ctx context.Context, tenantCode, deviceID string) error

	// ListByUser 返回某个用户的全部设备，供 web 端「设备管理」页展示。
	ListByUser(ctx context.Context, tenantCode, userID string) ([]Device, error)

	// Revoke 吊销一台设备。已连接的设备会在下次重连时被拒绝，正在保持的
	// 连接由 hub 侧单独断开。
	Revoke(ctx context.Context, tenantCode, deviceID string) error
}

// GenerateCode 生成一个 6 位数字配对码。
//
// 用 crypto/rand 而非 math/rand：配对码是能换取长期凭证的短期秘密，可预测
// 的随机源意味着攻击者能枚举出别人正在使用的码。6 位只有 100 万种组合，
// 因此有效期必须短（5 分钟）且**必须**在兑换端点上做速率限制，否则可被爆破。
func GenerateCode() (string, error) {
	const digits = 6
	max := big.NewInt(1_000_000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", fmt.Errorf("generate pairing code: %w", err)
	}
	return fmt.Sprintf("%0*d", digits, n.Int64()), nil
}

// GenerateToken 生成一个高熵的 device_token（256 bit，base64url 编码）。
func GenerateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate device token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashToken 返回 token 的 SHA-256 十六进制串，用于落库与查找。
//
// token 是 256 bit 的均匀随机值，不存在字典攻击面，因此单轮 SHA-256 足够
// ——不需要 bcrypt/argon2 那类为低熵人类密码设计的慢哈希。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqual 以恒定时间比较两个字符串，用于校验场景避免时序侧信道。
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
