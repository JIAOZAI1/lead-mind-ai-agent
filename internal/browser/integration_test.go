package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/browser/device"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/identity"
)

// 这些测试跑的是**真实的 WebSocket 连接**（httptest server + coder/websocket
// 客户端），用一个 mock 插件扮演浏览器端。它们覆盖单元测试到不了的地方：
// 认证握手的实际帧交换、指令下发到结果返回的完整往返、ack 延长审批超时的
// 时序行为。不依赖 Redis/MySQL——设备存储用内存实现替换。

// memDeviceStore 是 device.Store 的内存实现。
type memDeviceStore struct {
	devices []device.Device
}

func (m *memDeviceStore) Create(_ context.Context, _ string, d device.Device) error {
	m.devices = append(m.devices, d)
	return nil
}

func (m *memDeviceStore) FindByTokenHash(_ context.Context, _, tokenHash string) (device.Device, bool, error) {
	for _, d := range m.devices {
		if d.TokenHash == tokenHash {
			return d, true, nil
		}
	}
	return device.Device{}, false, nil
}

func (m *memDeviceStore) TouchLastSeen(_ context.Context, _, _ string) error { return nil }

func (m *memDeviceStore) ListByUser(_ context.Context, _, userID string) ([]device.Device, error) {
	var out []device.Device
	for _, d := range m.devices {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (m *memDeviceStore) Revoke(_ context.Context, _, deviceID string) error {
	now := time.Now()
	for i := range m.devices {
		if m.devices[i].ID == deviceID {
			m.devices[i].RevokedAt = &now
		}
	}
	return nil
}

// mockPlugin 是扮演浏览器插件的 WebSocket 客户端。
type mockPlugin struct {
	t  *testing.T
	ws *websocket.Conn
}

func (p *mockPlugin) send(kind MessageKind, payload any) {
	p.t.Helper()
	data, err := json.Marshal(newEnvelope(kind, payload))
	if err != nil {
		p.t.Fatalf("mock 插件序列化 %s 失败: %v", kind, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.ws.Write(ctx, websocket.MessageText, data); err != nil {
		p.t.Fatalf("mock 插件发送 %s 失败: %v", kind, err)
	}
}

func (p *mockPlugin) recv() rawEnvelope {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, data, err := p.ws.Read(ctx)
	if err != nil {
		p.t.Fatalf("mock 插件读取失败: %v", err)
	}
	env, err := decodeEnvelope(data)
	if err != nil {
		p.t.Fatalf("mock 插件解析失败: %v", err)
	}
	return env
}

// startTestServer 起一个只挂设备接入端点的 httptest server。
func startTestServer(t *testing.T, store device.Store) (*httptest.Server, *Hub) {
	t.Helper()

	hub := NewHub(HubConfig{
		Authenticator: NewStoreAuthenticator(store),
		Devices:       store,
		Logger:        testLogger(),
		AllowlistFor: func(context.Context, string, string) []string {
			return []string{"example.com"}
		},
	})

	handlers := &Handlers{Hub: hub, Devices: store, Logger: testLogger()}

	// 手工注入 identity，替代生产环境里 middleware.WithIdentity 的职责。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := identity.NewContext(r.Context(), identity.Identity{
			TenantCode: r.Header.Get("X-Tenant-Code"),
			UserID:     r.Header.Get("X-User-Id"),
		})
		handlers.Connect(w, r.WithContext(ctx))
	}))
	t.Cleanup(srv.Close)

	return srv, hub
}

// dialPlugin 建立 WS 连接并完成认证握手。
func dialPlugin(t *testing.T, srv *httptest.Server, tenantCode, token string) *mockPlugin {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"X-Tenant-Code": {tenantCode}, "X-User-Id": {"user-1"}},
	})
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	t.Cleanup(func() { ws.CloseNow() })

	plugin := &mockPlugin{t: t, ws: ws}
	plugin.send(KindAuth, AuthPayload{
		DeviceToken:   token,
		Capabilities:  []string{"read_page", "click", "list_tabs"},
		PluginVersion: "0.1.0",
		UserAgent:     "test",
	})
	return plugin
}

// seedDevice 在内存 store 里放一台已配对设备，返回明文 token。
func seedDevice(store *memDeviceStore) string {
	token := "test-device-token"
	store.devices = append(store.devices, device.Device{
		ID:        "device-1",
		UserID:    "user-1",
		Name:      "Chrome on Mac",
		TokenHash: device.HashToken(token),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	})
	return token
}

// TestHandshakeSucceeds 验证真实连接上的认证握手：插件发 auth，服务端回
// auth_ok 并带上 device_id / tenant_code / 心跳间隔。
func TestHandshakeSucceeds(t *testing.T) {
	store := &memDeviceStore{}
	token := seedDevice(store)
	srv, hub := startTestServer(t, store)

	plugin := dialPlugin(t, srv, "tenant-a", token)

	env := plugin.recv()
	if env.Kind != KindAuthOK {
		t.Fatalf("期望 auth_ok，收到 %s", env.Kind)
	}

	payload, err := decodePayload[AuthOKPayload](env)
	if err != nil {
		t.Fatalf("解析 auth_ok: %v", err)
	}
	if payload.DeviceID != "device-1" {
		t.Errorf("device_id = %q, want device-1", payload.DeviceID)
	}
	if payload.TenantCode != "tenant-a" {
		t.Errorf("tenant_code = %q, want tenant-a", payload.TenantCode)
	}
	if payload.UserID != "user-1" {
		t.Errorf("user_id = %q, want user-1", payload.UserID)
	}
	if payload.HeartbeatMS != int(heartbeatInterval/time.Millisecond) {
		t.Errorf("heartbeat_ms = %d, want %d", payload.HeartbeatMS, heartbeatInterval/time.Millisecond)
	}
	if len(payload.Allowlist) != 1 || payload.Allowlist[0] != "example.com" {
		t.Errorf("allowlist = %v, want [example.com]", payload.Allowlist)
	}

	// 连接应当已经登记进注册表，供指令下发使用。
	waitFor(t, func() bool {
		_, ok := hub.Lookup("tenant-a", "user-1", "device-1")
		return ok
	}, "连接没有被登记进 hub 注册表")
}

// TestHandshakeRejectsBadToken 无效 token 必须收到 auth_err 而不是被静默
// 断开——插件靠这一帧区分"该重新配对"与"该退避重连"。
func TestHandshakeRejectsBadToken(t *testing.T) {
	store := &memDeviceStore{}
	seedDevice(store)
	srv, _ := startTestServer(t, store)

	plugin := dialPlugin(t, srv, "tenant-a", "wrong-token")

	env := plugin.recv()
	if env.Kind != KindAuthErr {
		t.Fatalf("期望 auth_err，收到 %s", env.Kind)
	}
	payload, err := decodePayload[AuthErrPayload](env)
	if err != nil {
		t.Fatalf("解析 auth_err: %v", err)
	}
	if payload.Code != AuthErrInvalidToken {
		t.Errorf("code = %s, want %s", payload.Code, AuthErrInvalidToken)
	}
	if payload.Message == "" {
		t.Error("auth_err 必须带面向用户的说明文案")
	}
}

// TestHandshakeRejectsRevokedDevice 已吊销的设备必须拿到 TOKEN_REVOKED，
// 使插件清除本地凭证而不是无限重试。
func TestHandshakeRejectsRevokedDevice(t *testing.T) {
	store := &memDeviceStore{}
	token := seedDevice(store)
	revokedAt := time.Now()
	store.devices[0].RevokedAt = &revokedAt

	srv, _ := startTestServer(t, store)
	plugin := dialPlugin(t, srv, "tenant-a", token)

	env := plugin.recv()
	payload, err := decodePayload[AuthErrPayload](env)
	if err != nil {
		t.Fatalf("解析 auth_err: %v", err)
	}
	if payload.Code != AuthErrTokenRevoked {
		t.Errorf("code = %s, want %s", payload.Code, AuthErrTokenRevoked)
	}
}

// TestHandshakeRejectsExpiredDevice 过期凭证同理。
func TestHandshakeRejectsExpiredDevice(t *testing.T) {
	store := &memDeviceStore{}
	token := seedDevice(store)
	store.devices[0].ExpiresAt = time.Now().Add(-time.Hour)

	srv, _ := startTestServer(t, store)
	plugin := dialPlugin(t, srv, "tenant-a", token)

	payload, err := decodePayload[AuthErrPayload](plugin.recv())
	if err != nil {
		t.Fatalf("解析 auth_err: %v", err)
	}
	if payload.Code != AuthErrTokenExpired {
		t.Errorf("code = %s, want %s", payload.Code, AuthErrTokenExpired)
	}
}

// TestNonAuthFirstFrameIsRejected 认证完成前不接受任何其他消息——未认证
// 连接必须零权限。
func TestNonAuthFirstFrameIsRejected(t *testing.T) {
	store := &memDeviceStore{}
	seedDevice(store)
	srv, hub := startTestServer(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"X-Tenant-Code": {"tenant-a"}, "X-User-Id": {"user-1"}},
	})
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer ws.CloseNow()

	plugin := &mockPlugin{t: t, ws: ws}
	// 直接发 result，跳过认证。
	plugin.send(KindResult, ResultPayload{CmdID: "c1", OK: true})

	// 连接必须被断开，且绝不能出现在注册表里。
	if _, _, err := ws.Read(ctx); err == nil {
		t.Error("未认证连接发送非 auth 帧后应被断开")
	}
	if len(hub.OnlineDevices("tenant-a", "user-1")) != 0 {
		t.Error("未认证的连接不该出现在注册表里")
	}
}

// TestCommandRoundTrip 是最重要的端到端用例：Dispatcher 下发指令 → mock
// 插件收到 → 回 result → Dispatcher 拿到结果。
func TestCommandRoundTrip(t *testing.T) {
	store := &memDeviceStore{}
	token := seedDevice(store)
	srv, hub := startTestServer(t, store)

	plugin := dialPlugin(t, srv, "tenant-a", token)
	if env := plugin.recv(); env.Kind != KindAuthOK {
		t.Fatalf("握手失败，收到 %s", env.Kind)
	}
	waitFor(t, func() bool {
		_, ok := hub.Lookup("tenant-a", "user-1", "device-1")
		return ok
	}, "连接未登记")

	// mock 插件：收到 command 就回 ack + result。
	go func() {
		env := plugin.recv()
		if env.Kind != KindCommand {
			t.Errorf("插件期望收到 command，得到 %s", env.Kind)
			return
		}
		cmd, err := decodePayload[Command](env)
		if err != nil {
			t.Errorf("插件解析 command 失败: %v", err)
			return
		}
		plugin.send(KindAck, AckPayload{CmdID: cmd.CmdID, Status: AckAccepted})
		plugin.send(KindResult, ResultPayload{
			CmdID:      cmd.CmdID,
			OK:         true,
			Data:       map[string]any{"title": "Example", "url": "https://example.com/"},
			DurationMS: 12,
		})
	}()

	dispatcher := NewDispatcher(hub, testLogger())
	ctx := identity.NewContext(context.Background(), identity.Identity{
		TenantCode: "tenant-a",
		UserID:     "user-1",
	})

	result, err := dispatcher.Dispatch(ctx, CmdReadPage, map[string]any{"format": "text"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !result.OK {
		t.Fatalf("result.OK = false, error = %+v", result.Error)
	}

	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("data 类型 = %T, want map", result.Data)
	}
	if data["title"] != "Example" {
		t.Errorf("data.title = %v, want Example", data["title"])
	}
}

// TestDispatchRefusesCrossTenant 另一个租户的 identity 拿不到本租户的设备
// ——多租户红线的端到端确认。
func TestDispatchRefusesCrossTenant(t *testing.T) {
	store := &memDeviceStore{}
	token := seedDevice(store)
	srv, hub := startTestServer(t, store)

	plugin := dialPlugin(t, srv, "tenant-a", token)
	plugin.recv()
	waitFor(t, func() bool {
		_, ok := hub.Lookup("tenant-a", "user-1", "device-1")
		return ok
	}, "连接未登记")

	dispatcher := NewDispatcher(hub, testLogger())
	ctx := identity.NewContext(context.Background(), identity.Identity{
		TenantCode: "tenant-b",
		UserID:     "user-1",
	})

	if _, err := dispatcher.Dispatch(ctx, CmdReadPage, nil); err == nil {
		t.Fatal("另一个租户不该能驱动本租户的浏览器设备")
	}
}

// TestDispatchWithoutIdentityFails 没有租户身份时必须直接失败，绝不能"任选
// 一台在线设备"。
func TestDispatchWithoutIdentityFails(t *testing.T) {
	hub := newTestHub()
	dispatcher := NewDispatcher(hub, testLogger())

	if _, err := dispatcher.Dispatch(context.Background(), CmdReadPage, nil); err == nil {
		t.Fatal("缺少 identity 时必须报错")
	}
}

// TestDispatchRejectsUnsupportedCommand 插件未声明支持的指令应当直接被拦下，
// 而不是发出去干等到超时。
func TestDispatchRejectsUnsupportedCommand(t *testing.T) {
	store := &memDeviceStore{}
	token := seedDevice(store)
	srv, hub := startTestServer(t, store)

	plugin := dialPlugin(t, srv, "tenant-a", token)
	plugin.recv()
	waitFor(t, func() bool {
		_, ok := hub.Lookup("tenant-a", "user-1", "device-1")
		return ok
	}, "连接未登记")

	dispatcher := NewDispatcher(hub, testLogger())
	ctx := identity.NewContext(context.Background(), identity.Identity{
		TenantCode: "tenant-a",
		UserID:     "user-1",
	})

	// dialPlugin 声明的 capabilities 里没有 screenshot。
	_, err := dispatcher.Dispatch(ctx, CmdScreenshot, nil)
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("未声明支持的指令应被拦截，得到 err = %v", err)
	}
}

// TestApprovalAckExtendsTimeout 这条用例锁定审批时序：插件回
// ack{pending_approval} 后，服务端必须等到审批时长而不是在基础超时就放弃。
//
// 做法是让 mock 插件在超过基础超时之后才回 result。若延长逻辑失效，
// Execute 会提前返回超时错误，测试失败。
func TestApprovalAckExtendsTimeout(t *testing.T) {
	store := &memDeviceStore{}
	token := seedDevice(store)
	srv, hub := startTestServer(t, store)

	plugin := dialPlugin(t, srv, "tenant-a", token)
	plugin.recv()
	waitFor(t, func() bool {
		_, ok := hub.Lookup("tenant-a", "user-1", "device-1")
		return ok
	}, "连接未登记")

	const baseTimeout = 150 * time.Millisecond
	const humanDelay = 400 * time.Millisecond // 远超基础超时，模拟真人审批耗时

	go func() {
		env := plugin.recv()
		cmd, err := decodePayload[Command](env)
		if err != nil {
			t.Errorf("插件解析 command 失败: %v", err)
			return
		}
		// 高危指令：先告诉服务端"正在等用户审批"，把超时放宽。
		plugin.send(KindAck, AckPayload{
			CmdID:             cmd.CmdID,
			Status:            AckPendingApproval,
			ApprovalTimeoutMS: 5_000,
		})
		time.Sleep(humanDelay)
		plugin.send(KindResult, ResultPayload{CmdID: cmd.CmdID, OK: true, DurationMS: int64(humanDelay / time.Millisecond)})
	}()

	conn, _ := hub.Lookup("tenant-a", "user-1", "device-1")
	cmd := Command{CmdID: "cmd-approval", Type: CmdClick, Args: map[string]any{"element_ref": "e17"}, Risk: RiskHigh}

	result, err := conn.Execute(context.Background(), cmd, baseTimeout)
	if err != nil {
		t.Fatalf("ack{pending_approval} 应当把超时放宽到审批时长，却失败了: %v", err)
	}
	if !result.OK {
		t.Errorf("result.OK = false, want true")
	}
}

// TestExecuteTimesOutWithoutApprovalAck 没有 ack 延长时，基础超时必须生效
// ——这是上一条测试的对照组，确认延长不是"永远不超时"。
func TestExecuteTimesOutWithoutApprovalAck(t *testing.T) {
	store := &memDeviceStore{}
	token := seedDevice(store)
	srv, hub := startTestServer(t, store)

	plugin := dialPlugin(t, srv, "tenant-a", token)
	plugin.recv()
	waitFor(t, func() bool {
		_, ok := hub.Lookup("tenant-a", "user-1", "device-1")
		return ok
	}, "连接未登记")

	// 插件收下指令但从不回 result（模拟卡死/页面无响应）。
	go func() { plugin.recv() }()

	conn, _ := hub.Lookup("tenant-a", "user-1", "device-1")
	cmd := Command{CmdID: "cmd-stuck", Type: CmdReadPage, Args: map[string]any{}}

	started := time.Now()
	_, err := conn.Execute(context.Background(), cmd, 150*time.Millisecond)
	if err == nil {
		t.Fatal("没有收到 result 时必须超时失败")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("超时耗时 %s，远超设定的基础超时", elapsed)
	}
}

// TestPingGetsPong 心跳是 MV3 Service Worker 的保活手段，服务端必须回 pong。
func TestPingGetsPong(t *testing.T) {
	store := &memDeviceStore{}
	token := seedDevice(store)
	srv, _ := startTestServer(t, store)

	plugin := dialPlugin(t, srv, "tenant-a", token)
	plugin.recv() // auth_ok

	plugin.send(KindPing, struct{}{})

	if env := plugin.recv(); env.Kind != KindPong {
		t.Fatalf("ping 应当得到 pong，收到 %s", env.Kind)
	}
}

// TestMalformedFrameDoesNotDropConnection 一个畸形帧不能踢掉设备——否则
// 攻击者或有 bug 的插件版本能让设备陷入无限重连。
func TestMalformedFrameDoesNotDropConnection(t *testing.T) {
	store := &memDeviceStore{}
	token := seedDevice(store)
	srv, _ := startTestServer(t, store)

	plugin := dialPlugin(t, srv, "tenant-a", token)
	plugin.recv() // auth_ok

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := plugin.ws.Write(ctx, websocket.MessageText, []byte(`{"garbage"`)); err != nil {
		t.Fatalf("写入畸形帧失败: %v", err)
	}

	// 连接应当还活着：紧接着的 ping 仍能拿到 pong。
	plugin.send(KindPing, struct{}{})
	if env := plugin.recv(); env.Kind != KindPong {
		t.Fatalf("畸形帧之后连接应仍可用，收到 %s", env.Kind)
	}
}

// TestRevokeDisconnectsLiveConnection 吊销必须立刻切断已建立的连接。
func TestRevokeDisconnectsLiveConnection(t *testing.T) {
	store := &memDeviceStore{}
	token := seedDevice(store)
	srv, hub := startTestServer(t, store)

	plugin := dialPlugin(t, srv, "tenant-a", token)
	plugin.recv()
	waitFor(t, func() bool {
		_, ok := hub.Lookup("tenant-a", "user-1", "device-1")
		return ok
	}, "连接未登记")

	hub.Disconnect("tenant-a", "device-1", "device revoked")

	// 摘除必须是**同步立刻**完成的，不能等 WebSocket 的 close 握手
	// （对端不配合时最长 5 秒）——那几秒里被吊销的设备仍会被 Lookup 命中。
	if _, stillThere := hub.Lookup("tenant-a", "user-1", "device-1"); stillThere {
		t.Fatal("Disconnect 返回后设备仍能被 Lookup 命中，吊销没有立刻生效")
	}
	if len(hub.OnlineDevices("tenant-a", "user-1")) != 0 {
		t.Error("吊销后连接仍留在注册表里")
	}
}

// waitFor 轮询等待条件成立。用于等待"服务端 goroutine 完成注册"这类跨
// goroutine 的可见性，避免固定 sleep 造成的偶发失败。
func waitFor(t *testing.T, cond func() bool, failMsg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(failMsg)
}
