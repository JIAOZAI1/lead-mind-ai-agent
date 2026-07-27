package browser

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestConn 构造一个不带真实 WebSocket 的 Conn。
//
// 只用于测试注册表与 pending 表这类纯内存逻辑；任何触发 send 的路径都会
// 因为 ws 为 nil 而 panic，所以这些测试刻意不走发送路径。
func newTestConn(deviceID, tenantCode, userID string) *Conn {
	c := newConn(nil, deviceID, tenantCode, userID, nil, testLogger())
	return c
}

func newTestHub() *Hub {
	return NewHub(HubConfig{Logger: testLogger()})
}

// TestLookupIsolatesTenants 是本模块最重要的一条测试：同一个 device_id 在
// 不同租户下必须互不可见（PROJECT.md §4/§6.2 多租户红线）。
func TestLookupIsolatesTenants(t *testing.T) {
	hub := newTestHub()

	connA := newTestConn("device-1", "tenant-a", "user-1")
	connB := newTestConn("device-1", "tenant-b", "user-1")
	hub.register(connA)
	hub.register(connB)

	got, ok := hub.Lookup("tenant-a", "user-1", "device-1")
	if !ok || got != connA {
		t.Fatalf("tenant-a 查到的连接不是 connA")
	}

	got, ok = hub.Lookup("tenant-b", "user-1", "device-1")
	if !ok || got != connB {
		t.Fatalf("tenant-b 查到的连接不是 connB")
	}

	if _, ok := hub.Lookup("tenant-c", "user-1", "device-1"); ok {
		t.Error("tenant-c 不该查到任何连接")
	}
}

// TestLookupChecksUserOwnership device_id 是可枚举的，必须复核 user_id
// 归属，否则同租户下任意用户都能驱动别人的浏览器。
func TestLookupChecksUserOwnership(t *testing.T) {
	hub := newTestHub()
	hub.register(newTestConn("device-1", "tenant-a", "owner"))

	if _, ok := hub.Lookup("tenant-a", "attacker", "device-1"); ok {
		t.Error("另一个用户不该能按 device_id 查到别人的设备连接")
	}
	if _, ok := hub.Lookup("tenant-a", "owner", "device-1"); !ok {
		t.Error("设备所有者应该能查到自己的连接")
	}
}

// TestLookupAnyDeviceStaysScoped 不指定 device_id 时的"任选一台"也必须
// 限定在本租户+本用户范围内。
func TestLookupAnyDeviceStaysScoped(t *testing.T) {
	hub := newTestHub()
	hub.register(newTestConn("device-a", "tenant-a", "user-1"))
	hub.register(newTestConn("device-b", "tenant-b", "user-2"))

	conn, ok := hub.Lookup("tenant-a", "user-1", "")
	if !ok || conn.DeviceID() != "device-a" {
		t.Fatalf("tenant-a/user-1 应查到 device-a，实际 ok=%v", ok)
	}

	if _, ok := hub.Lookup("tenant-a", "user-2", ""); ok {
		t.Error("tenant-a 下不存在 user-2 的设备，不该查到 tenant-b 的连接")
	}
}

// TestRegisterSupersedesOldConnection 同一设备重连时，旧连接必须被摘除并
// 唤醒——否则指令会被下发到一条实际已死的连接上。
func TestRegisterSupersedesOldConnection(t *testing.T) {
	hub := newTestHub()

	old := newTestConn("device-1", "tenant-a", "user-1")
	hub.register(old)

	fresh := newTestConn("device-1", "tenant-a", "user-1")
	hub.register(fresh)

	got, ok := hub.Lookup("tenant-a", "user-1", "device-1")
	if !ok || got != fresh {
		t.Fatal("注册表里应当是新连接")
	}

	select {
	case <-old.closed:
	case <-time.After(time.Second):
		t.Error("旧连接没有被关闭，等待中的指令会一直挂着")
	}
}

// TestUnregisterKeepsNewerConnection 旧连接被替换后走到 unregister 时，
// 不能误删刚注册上来的新连接。
func TestUnregisterKeepsNewerConnection(t *testing.T) {
	hub := newTestHub()

	old := newTestConn("device-1", "tenant-a", "user-1")
	hub.register(old)
	fresh := newTestConn("device-1", "tenant-a", "user-1")
	hub.register(fresh)

	hub.unregister(old)

	if _, ok := hub.Lookup("tenant-a", "user-1", "device-1"); !ok {
		t.Error("摘除旧连接时误删了新连接")
	}
}

func TestOnlineDevicesScopedToTenantAndUser(t *testing.T) {
	hub := newTestHub()
	hub.register(newTestConn("device-a", "tenant-a", "user-1"))
	hub.register(newTestConn("device-b", "tenant-a", "user-1"))
	hub.register(newTestConn("device-c", "tenant-a", "user-2"))
	hub.register(newTestConn("device-d", "tenant-b", "user-1"))

	got := hub.OnlineDevices("tenant-a", "user-1")
	if len(got) != 2 {
		t.Fatalf("OnlineDevices 返回 %d 台设备，want 2：%v", len(got), got)
	}
	for _, id := range got {
		if id != "device-a" && id != "device-b" {
			t.Errorf("返回了不属于 tenant-a/user-1 的设备 %s", id)
		}
	}
}

func TestSupportsHonoursCapabilities(t *testing.T) {
	withCaps := newConn(nil, "d1", "t1", "u1", []string{"read_page", "click"}, testLogger())
	if !withCaps.Supports(CmdReadPage) {
		t.Error("声明支持的指令应当通过")
	}
	if withCaps.Supports(CmdScreenshot) {
		t.Error("未声明的指令应当被拦截")
	}

	// 未声明 capabilities（旧插件）时不拦截，交给插件自己回 UNSUPPORTED。
	noCaps := newConn(nil, "d1", "t1", "u1", nil, testLogger())
	if !noCaps.Supports(CmdScreenshot) {
		t.Error("插件未声明 capabilities 时不应在服务端侧拦截")
	}
}

// TestExecuteFailsFastWhenDeviceDisconnects 设备断开时，等待中的指令必须
// 立刻失败，而不是干等到超时——否则模型会被一条已经不可能返回的指令卡住。
func TestExecuteFailsFastWhenDeviceDisconnects(t *testing.T) {
	conn := newTestConn("device-1", "tenant-a", "user-1")

	// 手工登记一条 pending，跳过需要真实 ws 的 send。
	pending := &pendingCommand{
		result:           make(chan ResultPayload, 1),
		approvalExtended: make(chan struct{}),
	}
	conn.pending["cmd-1"] = pending

	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-pending.result:
		case <-conn.closed:
		case <-time.After(2 * time.Second):
			t.Error("连接关闭后等待没有被唤醒")
		}
	}()

	conn.close(1000, "test")

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("等待方没有在连接关闭后返回")
	}
}

// TestExtendForApprovalUnblocksOnce ack{pending_approval} 必须能放宽超时，
// 且重复的 ack 不会反复延长（extendOnce）。
func TestExtendForApprovalUnblocksOnce(t *testing.T) {
	conn := newTestConn("device-1", "tenant-a", "user-1")

	pending := &pendingCommand{
		result:           make(chan ResultPayload, 1),
		approvalExtended: make(chan struct{}),
	}
	conn.pending["cmd-1"] = pending

	conn.extendForApproval("cmd-1", 90*time.Second)

	select {
	case <-pending.approvalExtended:
	case <-time.After(time.Second):
		t.Fatal("approvalExtended 没有被触发，高危指令会在用户审批前就超时")
	}
	if pending.approvalTimeout != 90*time.Second {
		t.Errorf("approvalTimeout = %s, want 90s", pending.approvalTimeout)
	}

	// 第二次调用不能 panic（重复 close channel）。
	conn.extendForApproval("cmd-1", 30*time.Second)
	if pending.approvalTimeout != 90*time.Second {
		t.Errorf("重复 ack 不该改变已生效的审批超时，得到 %s", pending.approvalTimeout)
	}
}

// TestExtendForApprovalIgnoresUnknownCommand 对不存在的 cmd_id 不能 panic。
func TestExtendForApprovalIgnoresUnknownCommand(t *testing.T) {
	conn := newTestConn("device-1", "tenant-a", "user-1")
	conn.extendForApproval("no-such-command", time.Second)
}

// TestDeliverResultDropsUnmatched 插件重连后补发的、已无人等待的 result
// 必须被安静丢弃而不是阻塞或 panic。
func TestDeliverResultDropsUnmatched(t *testing.T) {
	conn := newTestConn("device-1", "tenant-a", "user-1")
	conn.deliverResult(ResultPayload{CmdID: "unknown", OK: true})
}

func TestDeliverResultReachesWaiter(t *testing.T) {
	conn := newTestConn("device-1", "tenant-a", "user-1")

	pending := &pendingCommand{
		result:           make(chan ResultPayload, 1),
		approvalExtended: make(chan struct{}),
	}
	conn.pending["cmd-1"] = pending

	conn.deliverResult(ResultPayload{CmdID: "cmd-1", OK: true, DurationMS: 42})

	select {
	case got := <-pending.result:
		if !got.OK || got.DurationMS != 42 {
			t.Errorf("result = %+v, want ok=true duration=42", got)
		}
	case <-time.After(time.Second):
		t.Fatal("result 没有送达等待方")
	}
}

// TestDisconnectClosesTargetedConnection 吊销设备时必须能立刻切断已建立的
// 连接——只写 revoked_at 不足以阻止一台已连上的设备继续接受指令。
func TestDisconnectClosesTargetedConnection(t *testing.T) {
	hub := newTestHub()
	conn := newTestConn("device-1", "tenant-a", "user-1")
	hub.register(conn)

	hub.Disconnect("tenant-a", "device-1", "revoked")

	select {
	case <-conn.closed:
	case <-time.After(time.Second):
		t.Fatal("Disconnect 没有关闭目标连接")
	}
}

// TestDisconnectIgnoresOtherTenants 跨租户的 Disconnect 不该切断别人的连接。
func TestDisconnectIgnoresOtherTenants(t *testing.T) {
	hub := newTestHub()
	conn := newTestConn("device-1", "tenant-a", "user-1")
	hub.register(conn)

	hub.Disconnect("tenant-b", "device-1", "revoked")

	select {
	case <-conn.closed:
		t.Fatal("另一个租户的 Disconnect 切断了本租户的连接")
	case <-time.After(50 * time.Millisecond):
	}
}
