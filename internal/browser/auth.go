package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/browser/device"
)

// defaultApprovalTimeout 是插件未在 ack 里给出审批时长时的兜底值，与插件侧
// ApprovalCard 的 90 秒超时保持一致。
const defaultApprovalTimeout = 90 * time.Second

// StoreAuthenticator 用租户库里的设备表校验 device_token。
type StoreAuthenticator struct {
	devices device.Store
}

// NewStoreAuthenticator 构建一个 Authenticator。
func NewStoreAuthenticator(devices device.Store) *StoreAuthenticator {
	return &StoreAuthenticator{devices: devices}
}

// Authenticate 校验 token 并返回它绑定的设备。
//
// 返回的第二个值是给插件的失败原因：非空表示"凭证有问题"（插件应清除本地
// token 并要求重新配对），为空但 err 非空表示服务端内部故障（插件应退避重试）。
// 这个区分很重要——把内部故障当成凭证失效会让所有设备在一次数据库抖动后
// 集体要求用户重新配对。
func (a *StoreAuthenticator) Authenticate(ctx context.Context, tenantCode, token string) (device.Device, AuthErrCode, error) {
	if token == "" {
		return device.Device{}, AuthErrInvalidToken, fmt.Errorf("empty device token")
	}

	// 按哈希查找而非按明文比对：库里存的就是哈希，索引命中即完成校验。
	// 这天然是恒定时间的——查不到就是查不到，不存在逐字节比较的时序泄露。
	dev, found, err := a.devices.FindByTokenHash(ctx, tenantCode, device.HashToken(token))
	if err != nil {
		return device.Device{}, "", fmt.Errorf("look up device by token for tenant %s: %w", tenantCode, err)
	}
	if !found {
		return device.Device{}, AuthErrInvalidToken, fmt.Errorf("no device matches the presented token for tenant %s", tenantCode)
	}
	if dev.Revoked() {
		return device.Device{}, AuthErrTokenRevoked, fmt.Errorf("device %s has been revoked", dev.ID)
	}
	if dev.Expired(time.Now()) {
		return device.Device{}, AuthErrTokenExpired, fmt.Errorf("device %s credential expired at %s", dev.ID, dev.ExpiresAt.UTC().Format(time.RFC3339))
	}

	return dev, "", nil
}
