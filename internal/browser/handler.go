package browser

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/browser/device"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/identity"
)

// DeviceTokenTTL 是 device_token 的有效期。30 天在"别老让用户重新配对"与
// "凭证泄露后的暴露窗口"之间取平衡；用户随时可在 web 端主动吊销（插件
// DESIGN.md §7.1）。
const DeviceTokenTTL = 30 * 24 * time.Hour

// PairingCodeTTL 是一次性配对码的有效期。
//
// 5 分钟是安全上的硬要求而非体验取舍：配对码只有 6 位（100 万种组合），
// 有效期越长，可被在线爆破的窗口越大。缩短窗口 + 端点限流是这个短码能成立
// 的前提。
const PairingCodeTTL = 5 * time.Minute

// Handlers 提供设备配对与 WebSocket 接入的 HTTP 端点。
type Handlers struct {
	Hub     *Hub
	Codes   device.CodeStore
	Devices device.Store
	Logger  *slog.Logger

	// AllowedOrigins 是允许发起 WebSocket 连接的 Origin 列表。
	//
	// 扩展的 Origin 形如 chrome-extension://<id>。留空表示不校验 Origin
	// ——仅适用于本地开发，生产环境必须显式配置，否则任意网页都能从用户
	// 浏览器里发起到本服务的 WS 连接（CSWSH）。
	AllowedOrigins []string
}

func (h *Handlers) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// ---------------------------------------------------------------------------
// 配对码签发（web 端调用，用户已登录）
// ---------------------------------------------------------------------------

type issueCodeResponse struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
	ExpiresIn int    `json:"expires_in_seconds"`
}

// IssuePairingCode 为当前登录用户签发一个一次性配对码。
//
// 由 lead-mind web 端在用户已登录的会话中调用，身份来自上游注入的
// X-Tenant-Code / X-User-Id——插件自己拿不到这个端点所需的身份。
func (h *Handlers) IssuePairingCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := identity.FromContext(ctx)

	if id.UserID == "" {
		// 配对码把设备绑定到具体的人。没有 user_id 就无从绑定，也就无法在
		// "设备管理"页归属到某个用户——这种调用必须拒绝而不是绑定到空用户。
		h.writeError(ctx, w, r, errors.New("pairing requires an authenticated user"),
			"pairing requires an authenticated user", http.StatusBadRequest)
		return
	}

	code, err := device.GenerateCode()
	if err != nil {
		h.writeError(ctx, w, r, err, "failed to generate pairing code", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().Add(PairingCodeTTL)
	record := device.PairingCode{
		Code:       code,
		TenantCode: id.TenantCode,
		UserID:     id.UserID,
		ExpiresAt:  expiresAt,
	}
	if err := h.Codes.Issue(ctx, record, PairingCodeTTL); err != nil {
		h.writeError(ctx, w, r, err, "failed to issue pairing code", http.StatusInternalServerError)
		return
	}

	h.logger().InfoContext(ctx, "已签发设备配对码",
		slog.String("tenant_code", id.TenantCode),
		slog.String("user_id", id.UserID))

	// 响应体里带的是配对码本身——这是它唯一一次出现的地方，属于必要暴露；
	// 但绝不写进日志（上面那条 InfoContext 刻意不带 code 字段）。
	h.writeJSON(ctx, w, http.StatusOK, issueCodeResponse{
		Code:      code,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
		ExpiresIn: int(PairingCodeTTL / time.Second),
	})
}

// ---------------------------------------------------------------------------
// 配对码兑换（插件调用，无身份）
// ---------------------------------------------------------------------------

type pairRequest struct {
	Code              string `json:"code"`
	DeviceName        string `json:"device_name"`
	DeviceFingerprint string `json:"device_fingerprint"`
}

type pairResponse struct {
	DeviceToken string   `json:"device_token"`
	DeviceID    string   `json:"device_id"`
	TenantCode  string   `json:"tenant_code"`
	UserID      string   `json:"user_id"`
	Allowlist   []string `json:"allowlist,omitempty"`
}

// Pair 用一次性配对码换取长期 device_token。
//
// 这是**唯一一个不要求上游注入身份的端点**：插件在配对前还没有任何凭证，
// 拿不到 X-Tenant-Code。租户身份完全由配对码本身携带——因此配对码的一次性
// 与不可预测性就是这里的全部安全保证（见 device.GenerateCode 的说明）。
func (h *Handlers) Pair(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req pairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		h.writeError(ctx, w, r, err, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.Code) != 6 {
		h.writeError(ctx, w, r, errors.New("pairing code must be 6 digits"),
			"invalid pairing code", http.StatusBadRequest)
		return
	}

	// 插件不带 X-Tenant-Code（配对前它还不知道自己属于哪个租户），所以租户
	// 必须从配对码反查。TenantCode 若由上游注入则优先使用——那说明这次配对
	// 是从已认证的上下文里发起的。
	id, _ := identity.FromContext(ctx)
	tenantCode := id.TenantCode
	if tenantCode == "" {
		h.writeError(ctx, w, r, errors.New("cannot resolve tenant for pairing request"),
			"pairing requires a tenant context", http.StatusBadRequest)
		return
	}

	pairing, found, err := h.Codes.Redeem(ctx, tenantCode, req.Code)
	if err != nil {
		h.writeError(ctx, w, r, err, "failed to redeem pairing code", http.StatusInternalServerError)
		return
	}
	if !found {
		// 404 与插件 describePairFailure() 的文案对应："配对码不存在或已过期"。
		// 刻意不区分"不存在"与"已被兑换"——区分开会给爆破者一个探测信号。
		h.writeError(ctx, w, r, errors.New("pairing code not found or already redeemed"),
			"pairing code not found or expired", http.StatusNotFound)
		return
	}

	token, err := device.GenerateToken()
	if err != nil {
		h.writeError(ctx, w, r, err, "failed to issue device token", http.StatusInternalServerError)
		return
	}

	dev := device.Device{
		ID:          uuid.NewString(),
		UserID:      pairing.UserID,
		Name:        req.DeviceName,
		Fingerprint: req.DeviceFingerprint,
		TokenHash:   device.HashToken(token),
		ExpiresAt:   time.Now().Add(DeviceTokenTTL),
	}
	if err := h.Devices.Create(ctx, pairing.TenantCode, dev); err != nil {
		h.writeError(ctx, w, r, err, "failed to register device", http.StatusInternalServerError)
		return
	}

	h.logger().InfoContext(ctx, "浏览器设备配对成功",
		slog.String("tenant_code", pairing.TenantCode),
		slog.String("user_id", pairing.UserID),
		slog.String("device_id", dev.ID))

	var allowlist []string
	if h.Hub != nil && h.Hub.allowlistFor != nil {
		allowlist = h.Hub.allowlistFor(ctx, pairing.TenantCode, pairing.UserID)
	}

	// device_token 明文只在这里出现这一次，之后服务端只存哈希、无法再取回。
	h.writeJSON(ctx, w, http.StatusOK, pairResponse{
		DeviceToken: token,
		DeviceID:    dev.ID,
		TenantCode:  pairing.TenantCode,
		UserID:      pairing.UserID,
		Allowlist:   allowlist,
	})
}

// ---------------------------------------------------------------------------
// WebSocket 接入
// ---------------------------------------------------------------------------

// Connect 把 HTTP 连接升级为 WebSocket，并交给 Hub 处理。
//
// 认证不在这里做——它发生在升级之后的首帧 auth 消息里（插件 DESIGN.md §3.1）。
// 因此本端点会为一个尚未认证的对端建立连接，Hub 必须在 authTimeout 内完成
// 认证否则断开。
func (h *Handlers) Connect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := identity.FromContext(ctx)

	if id.TenantCode == "" {
		h.writeError(ctx, w, r, errors.New("missing tenant code on device connect"),
			"missing X-Tenant-Code", http.StatusBadRequest)
		return
	}

	opts := &websocket.AcceptOptions{
		OriginPatterns: h.AllowedOrigins,
	}
	// 扩展页面的 Origin 是 chrome-extension://<id>，不同于普通网页。留空
	// AllowedOrigins 时只能靠 InsecureSkipVerify 放行——这仅供本地开发，
	// 生产必须配置 AllowedOrigins（见字段注释）。
	if len(h.AllowedOrigins) == 0 {
		opts.InsecureSkipVerify = true
	}

	ws, err := websocket.Accept(w, r, opts)
	if err != nil {
		// Accept 失败时它已经自行写过 HTTP 错误响应，这里只补一条服务端日志。
		h.logger().WarnContext(ctx, "WebSocket 升级失败",
			slog.String("tenant_code", id.TenantCode),
			slog.String("origin", r.Header.Get("Origin")),
			slog.String("error", err.Error()))
		return
	}

	// Serve 会一直阻塞到连接断开。用 context.WithoutCancel 脱离请求 context：
	// 某些 HTTP server 实现会在 handler 视角的请求"结束"时取消 r.Context()，
	// 而这条连接的生命周期本就长于一次请求语义。
	serveCtx := context.WithoutCancel(ctx)
	h.Hub.Serve(serveCtx, ws, id.TenantCode)
}

// ---------------------------------------------------------------------------
// 设备管理（web 端调用）
// ---------------------------------------------------------------------------

type deviceView struct {
	DeviceID   string `json:"device_id"`
	Name       string `json:"name"`
	Online     bool   `json:"online"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
	Revoked    bool   `json:"revoked"`
}

// ListDevices 返回当前用户已配对的设备，供 web 端「设备管理」页展示。
func (h *Handlers) ListDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := identity.FromContext(ctx)

	devices, err := h.Devices.ListByUser(ctx, id.TenantCode, id.UserID)
	if err != nil {
		h.writeError(ctx, w, r, err, "failed to list devices", http.StatusInternalServerError)
		return
	}

	online := make(map[string]struct{})
	for _, deviceID := range h.Hub.OnlineDevices(id.TenantCode, id.UserID) {
		online[deviceID] = struct{}{}
	}

	views := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		_, isOnline := online[d.ID]
		views = append(views, deviceView{
			DeviceID:   d.ID,
			Name:       d.Name,
			Online:     isOnline,
			CreatedAt:  d.CreatedAt.UTC().Format(time.RFC3339),
			LastSeenAt: d.LastSeenAt.UTC().Format(time.RFC3339),
			ExpiresAt:  d.ExpiresAt.UTC().Format(time.RFC3339),
			Revoked:    d.Revoked(),
		})
	}

	h.writeJSON(ctx, w, http.StatusOK, map[string]any{"devices": views})
}

// RevokeDevice 吊销一台设备。
func (h *Handlers) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := identity.FromContext(ctx)

	deviceID := r.PathValue("id")
	if deviceID == "" {
		h.writeError(ctx, w, r, errors.New("missing device id"), "missing device id", http.StatusBadRequest)
		return
	}

	// 先校验归属再吊销：ListByUser 已按 user_id 过滤，因此在结果里找不到就
	// 说明这台设备不属于当前用户。少了这一步，同租户下任意用户都能凭 ID
	// 吊销别人的设备。
	devices, err := h.Devices.ListByUser(ctx, id.TenantCode, id.UserID)
	if err != nil {
		h.writeError(ctx, w, r, err, "failed to look up device", http.StatusInternalServerError)
		return
	}
	owned := false
	for _, d := range devices {
		if d.ID == deviceID {
			owned = true
			break
		}
	}
	if !owned {
		h.writeError(ctx, w, r, errors.New("device does not belong to the caller"),
			"device not found", http.StatusNotFound)
		return
	}

	if err := h.Devices.Revoke(ctx, id.TenantCode, deviceID); err != nil {
		h.writeError(ctx, w, r, err, "failed to revoke device", http.StatusInternalServerError)
		return
	}

	// 只写 revoked_at 不足以阻止一台**已经连上**的设备继续接受指令——它下次
	// 重连才会被拒。必须立刻切断当前连接。
	h.Hub.Disconnect(id.TenantCode, deviceID, "device revoked")

	h.logger().InfoContext(ctx, "设备已吊销",
		slog.String("tenant_code", id.TenantCode),
		slog.String("user_id", id.UserID),
		slog.String("device_id", deviceID))

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 响应辅助
// ---------------------------------------------------------------------------

func (h *Handlers) writeJSON(ctx context.Context, w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// 响应头已提交，无法再改成错误响应——只能记日志（PROJECT.md §6.5
		// 禁止静默失败）。
		h.logger().ErrorContext(ctx, "写入响应体失败", slog.String("error", err.Error()))
	}
}

// writeError 与 gateway/handler.httpError 同构：给客户端稳定的通用文案，
// 把真实错误留在服务端日志里（PROJECT.md §6.5）。
func (h *Handlers) writeError(ctx context.Context, w http.ResponseWriter, r *http.Request, err error, msg string, status int) {
	id, _ := identity.FromContext(ctx)

	level := slog.LevelError
	if status < http.StatusInternalServerError {
		level = slog.LevelWarn
	}
	h.logger().Log(ctx, level, "device endpoint error",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("tenant_code", id.TenantCode),
		slog.String("user_id", id.UserID),
		slog.Int("status", status),
		slog.String("error", err.Error()))

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
