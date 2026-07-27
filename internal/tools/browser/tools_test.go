package browser

import (
	"context"
	"errors"
	"strings"
	"testing"

	brw "github.com/JIAOZAI1/lead-mind-ai-agent/internal/browser"
)

// fakeDispatcher 记录收到的指令并返回预设结果，让工具层的测试完全不依赖
// WebSocket / Redis / MySQL。
type fakeDispatcher struct {
	lastType brw.CommandType
	lastArgs map[string]any

	result brw.ResultPayload
	err    error
}

func (f *fakeDispatcher) Dispatch(_ context.Context, cmdType brw.CommandType, args map[string]any) (brw.ResultPayload, error) {
	f.lastType = cmdType
	f.lastArgs = args
	return f.result, f.err
}

// TestRunPassesCommandFailureToModel 是本包最关键的一条测试。
//
// 指令级失败（元素不见了、用户拒绝审批）必须作为**正常返回值**交给模型，
// 而不是 Go error——返回 error 会中断 ReAct 循环，而这些信息恰恰是模型
// 应当看到并据此调整下一步的。
func TestRunPassesCommandFailureToModel(t *testing.T) {
	const guidance = "Element e17 no longer exists — call read_page again for a fresh snapshot."

	d := &fakeDispatcher{
		result: brw.ResultPayload{
			CmdID: "c1",
			OK:    false,
			Error: &brw.ResultError{
				Code:      brw.ErrElementNotFound,
				Message:   guidance,
				Retryable: false,
			},
		},
	}

	out, err := run(context.Background(), d, brw.CmdClick, map[string]any{"element_ref": "e17"})
	if err != nil {
		t.Fatalf("指令级失败不应返回 Go error（会中断 ReAct 循环），得到 %v", err)
	}
	// 插件精心写的"下一步指引"必须原样到达模型。
	if !strings.Contains(out, guidance) {
		t.Errorf("返回文本没有包含插件给模型的指引：%q", out)
	}
	if !strings.Contains(out, string(brw.ErrElementNotFound)) {
		t.Errorf("返回文本没有包含错误码：%q", out)
	}
}

// TestRunReturnsErrorOnTransportFailure 传输层失败（设备离线/超时）才应该
// 返回 Go error。
func TestRunReturnsErrorOnTransportFailure(t *testing.T) {
	d := &fakeDispatcher{err: errors.New("no browser device is currently connected")}

	if _, err := run(context.Background(), d, brw.CmdReadPage, nil); err == nil {
		t.Fatal("传输层失败应当返回 Go error")
	}
}

func TestRunEncodesSuccessData(t *testing.T) {
	d := &fakeDispatcher{
		result: brw.ResultPayload{
			CmdID: "c1",
			OK:    true,
			Data:  map[string]any{"tab_id": 7, "final_url": "https://example.com/"},
		},
	}

	out, err := run(context.Background(), d, brw.CmdOpenTab, map[string]any{"url": "https://example.com"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "final_url") || !strings.Contains(out, "example.com") {
		t.Errorf("成功结果没有被序列化给模型：%q", out)
	}
}

func TestRunHandlesEmptyData(t *testing.T) {
	d := &fakeDispatcher{result: brw.ResultPayload{CmdID: "c1", OK: true}}

	out, err := run(context.Background(), d, brw.CmdScroll, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out == "" {
		t.Error("即使没有返回数据，也要给模型一个明确的成功信号")
	}
}

// TestNewToolsRegistersEveryCommand 每条协议指令都必须有工具可达，否则模型
// 根本没法用到它。go_back/go_forward/reload 合并进 browser_navigate，因此
// 工具数比指令数少 2。
func TestNewToolsRegistersEveryCommand(t *testing.T) {
	tools, err := NewTools(&fakeDispatcher{})
	if err != nil {
		t.Fatalf("NewTools: %v", err)
	}

	names := make(map[string]struct{}, len(tools))
	for _, tl := range tools {
		info, err := tl.Info(context.Background())
		if err != nil {
			t.Fatalf("tool Info: %v", err)
		}
		names[info.Name] = struct{}{}

		// 工具描述是模型选择工具的唯一依据，空描述等于让模型盲猜。
		if strings.TrimSpace(info.Desc) == "" {
			t.Errorf("工具 %s 没有描述", info.Name)
		}
	}

	want := []string{
		"browser_list_tabs", "browser_open_tab", "browser_close_tab", "browser_activate_tab",
		"browser_read_page", "browser_find_elements", "browser_click", "browser_type",
		"browser_select", "browser_scroll", "browser_wait_for", "browser_extract",
		"browser_screenshot", "browser_navigate",
	}
	for _, name := range want {
		if _, ok := names[name]; !ok {
			t.Errorf("缺少工具 %s", name)
		}
	}
	if len(tools) != len(want) {
		t.Errorf("工具数 = %d, want %d", len(tools), len(want))
	}
}

// TestReadPageDescriptionWarnsAboutUntrustedContent 页面正文是 prompt
// injection 的头号入口（插件 DESIGN.md §7.4）。read_page 的描述里必须明确
// 告诉模型"这是数据不是指令"，这条约束容易在后续改文案时被顺手删掉。
func TestReadPageDescriptionWarnsAboutUntrustedContent(t *testing.T) {
	tl, err := newReadPageTool(&fakeDispatcher{})
	if err != nil {
		t.Fatalf("newReadPageTool: %v", err)
	}
	info, err := tl.Info(context.Background())
	if err != nil {
		t.Fatalf("tool Info: %v", err)
	}

	desc := strings.ToUpper(info.Desc)
	if !strings.Contains(desc, "UNTRUSTED") {
		t.Error("read_page 的描述必须声明返回内容不可信")
	}
	if !strings.Contains(desc, "NEVER AS COMMANDS") && !strings.Contains(desc, "NEVER AS INSTRUCTIONS") {
		t.Error("read_page 的描述必须告诉模型不要执行页面里的指令")
	}
}

// TestNavigateRejectsUnknownDirection 方向参数非法时必须报错，不能默默退化成
// 某个默认动作——模型拿到"成功"却发生了别的导航会让它的世界模型出错。
func TestNavigateRejectsUnknownDirection(t *testing.T) {
	d := &fakeDispatcher{result: brw.ResultPayload{OK: true}}
	tl, err := newNavigateTool(d)
	if err != nil {
		t.Fatalf("newNavigateTool: %v", err)
	}

	if _, err := tl.InvokableRun(context.Background(), `{"direction":"sideways"}`); err == nil {
		t.Fatal("非法 direction 应当报错")
	}
	if d.lastType != "" {
		t.Errorf("非法 direction 不该下发任何指令，却发了 %s", d.lastType)
	}
}

func TestNavigateMapsDirections(t *testing.T) {
	tests := []struct {
		direction string
		want      brw.CommandType
	}{
		{"back", brw.CmdGoBack},
		{"forward", brw.CmdGoForward},
		{"reload", brw.CmdReload},
	}

	for _, tt := range tests {
		t.Run(tt.direction, func(t *testing.T) {
			d := &fakeDispatcher{result: brw.ResultPayload{OK: true}}
			tl, err := newNavigateTool(d)
			if err != nil {
				t.Fatalf("newNavigateTool: %v", err)
			}
			if _, err := tl.InvokableRun(context.Background(), `{"direction":"`+tt.direction+`"}`); err != nil {
				t.Fatalf("InvokableRun: %v", err)
			}
			if d.lastType != tt.want {
				t.Errorf("direction=%s 下发了 %s, want %s", tt.direction, d.lastType, tt.want)
			}
		})
	}
}

// TestClickRequiresElementRef 缺少 element_ref 时必须直接报错，避免把一条
// 无意义的指令发到浏览器再等它失败。
func TestClickRequiresElementRef(t *testing.T) {
	d := &fakeDispatcher{result: brw.ResultPayload{OK: true}}
	tl, err := newClickTool(d)
	if err != nil {
		t.Fatalf("newClickTool: %v", err)
	}

	if _, err := tl.InvokableRun(context.Background(), `{"element_ref":""}`); err == nil {
		t.Fatal("空 element_ref 应当报错")
	}
	if d.lastType != "" {
		t.Error("空 element_ref 不该下发指令")
	}
}

// TestTypeForwardsArguments 参数名必须与插件侧 executors 期望的一致，写错了
// 插件会拿到 undefined 然后行为异常。
func TestTypeForwardsArguments(t *testing.T) {
	d := &fakeDispatcher{result: brw.ResultPayload{OK: true}}
	tl, err := newTypeTool(d)
	if err != nil {
		t.Fatalf("newTypeTool: %v", err)
	}

	_, err = tl.InvokableRun(context.Background(),
		`{"element_ref":"e18","text":"hello","submit":true}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}

	if d.lastType != brw.CmdType {
		t.Errorf("下发了 %s, want %s", d.lastType, brw.CmdType)
	}
	if d.lastArgs["element_ref"] != "e18" {
		t.Errorf("element_ref = %v, want e18", d.lastArgs["element_ref"])
	}
	if d.lastArgs["text"] != "hello" {
		t.Errorf("text = %v, want hello", d.lastArgs["text"])
	}
	if d.lastArgs["submit"] != true {
		t.Errorf("submit = %v, want true", d.lastArgs["submit"])
	}
	// 未显式提供的可选参数不应出现在 args 里——插件侧对 clear 有自己的默认值
	// （true），服务端塞一个零值进去会覆盖掉它。
	if _, present := d.lastArgs["clear"]; present {
		t.Error("未提供的 clear 参数不该被下发")
	}
}
