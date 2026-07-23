// Package session 管理会话身份与元数据：生成/解析 session ID，以及
// （在 store.go 中）支撑会话列表 UI 的持久化会话记录（标题、置顶/归档
// 状态、活跃时间戳）。本包本身不持有对话内容——那是
// internal/memory/shortterm 的职责。
package session

import "github.com/google/uuid"

// New 生成一个新的服务端 session ID。
func New() string {
	return uuid.NewString()
}

// Resolve 如果 clientSuppliedID 非空则原样返回（客户端驱动的恢复已有
// 会话场景），否则生成一个新的 ID。一个尚无匹配历史记录/元数据的
// "恢复型" ID，其行为等同于一个全新会话——本包中任何地方都不会将其
// 视为错误。
func Resolve(clientSuppliedID string) string {
	if clientSuppliedID != "" {
		return clientSuppliedID
	}
	return New()
}
