package browser

import (
	"encoding/json"
	"testing"
	"time"
)

// TestRiskTableMatchesPlugin 锁定服务端风险表与插件 src/shared/risk.ts 一致。
//
// 两侧不一致本身不会造成安全漏洞（插件不采信服务端的标注，最终判定在插件
// 本地），但会让插件持续告警"风险等级不匹配"，说明契约已经漂移。
func TestRiskTableMatchesPlugin(t *testing.T) {
	expected := map[CommandType]RiskLevel{
		"list_tabs":     RiskLow,
		"activate_tab":  RiskLow,
		"close_tab":     RiskLow,
		"read_page":     RiskLow,
		"find_elements": RiskLow,
		"scroll":        RiskLow,
		"wait_for":      RiskLow,
		"extract":       RiskLow,
		"screenshot":    RiskLow,
		"go_back":       RiskLow,
		"go_forward":    RiskLow,
		"reload":        RiskLow,
		"click":         RiskHigh,
		"type":          RiskHigh,
		"select":        RiskHigh,
		"open_tab":      RiskHigh,
	}

	if len(riskTable) != len(expected) {
		t.Fatalf("风险表条目数 = %d，插件侧为 %d——两端指令集已漂移", len(riskTable), len(expected))
	}

	for cmd, want := range expected {
		if got := RiskOf(cmd); got != want {
			t.Errorf("RiskOf(%s) = %s, want %s", cmd, got, want)
		}
	}
}

// TestRiskOfUnknownCommandIsHigh 未登记的指令必须按高危处理，与插件侧兜底一致。
func TestRiskOfUnknownCommandIsHigh(t *testing.T) {
	if got := RiskOf("some_future_command"); got != RiskHigh {
		t.Errorf("RiskOf(未知指令) = %s, want %s", got, RiskHigh)
	}
}

// TestEnvelopeRoundTrip 验证信封的字段名与插件侧 Envelope 接口一致。
//
// 字段名写错不会导致编译失败，只会让插件收到一个字段全是 undefined 的对象
// ——正是文件头警告的"静默解析失败"，所以必须用测试锁住。
func TestEnvelopeRoundTrip(t *testing.T) {
	cmd := Command{
		CmdID: "cmd-1",
		Type:  CmdClick,
		Args:  map[string]any{"element_ref": "e17"},
		Risk:  RiskHigh,
	}

	data, err := marshalEnvelope(newEnvelope(KindCommand, cmd))
	if err != nil {
		t.Fatalf("marshalEnvelope: %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}

	for _, field := range []string{"v", "id", "ts", "kind", "payload"} {
		if _, ok := wire[field]; !ok {
			t.Errorf("信封缺少字段 %q，插件侧会解析失败", field)
		}
	}
	if wire["v"] != float64(ProtocolVersion) {
		t.Errorf("v = %v, want %d", wire["v"], ProtocolVersion)
	}
	if wire["kind"] != string(KindCommand) {
		t.Errorf("kind = %v, want %s", wire["kind"], KindCommand)
	}

	payload, ok := wire["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload 不是对象: %T", wire["payload"])
	}
	if payload["cmd_id"] != "cmd-1" {
		t.Errorf("payload.cmd_id = %v, want cmd-1", payload["cmd_id"])
	}
	// 插件用 args.element_ref 定位元素，字段名错了点击就永远找不到目标。
	args, ok := payload["args"].(map[string]any)
	if !ok || args["element_ref"] != "e17" {
		t.Errorf("payload.args = %v, want element_ref=e17", payload["args"])
	}
}

// TestTimestampFormatIsRFC3339WithOffset 时间戳必须带时区偏移（PROJECT.md §6.6）。
func TestTimestampFormatIsRFC3339WithOffset(t *testing.T) {
	ts := nowTS()
	if _, err := time.Parse(RFC3339Milli, ts); err != nil {
		t.Fatalf("时间戳 %q 不符合协议格式: %v", ts, err)
	}
	// RFC3339 解析器接受它，说明偏移量存在且合法（Z 或 ±hh:mm）。
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("时间戳 %q 不是合法的 RFC3339: %v", ts, err)
	}
}

func TestDecodeEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{
			name: "合法帧",
			raw:  `{"v":1,"id":"m1","ts":"2026-07-27T10:00:00.000+08:00","kind":"result","payload":{}}`,
		},
		{
			name:    "非法 JSON",
			raw:     `{not json`,
			wantErr: true,
		},
		{
			name:    "缺少 kind",
			raw:     `{"v":1,"id":"m1","payload":{}}`,
			wantErr: true,
		},
		{
			name:    "缺少 id",
			raw:     `{"v":1,"kind":"result","payload":{}}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeEnvelope([]byte(tt.raw))
			if tt.wantErr != (err != nil) {
				t.Fatalf("decodeEnvelope err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

// TestDecodePayloadPreservesIntegers 确认 tab_id 这类整数不会被 JSON 的
// float64 默认解析破坏——这正是 rawEnvelope 保留 json.RawMessage 的原因。
func TestDecodePayloadPreservesIntegers(t *testing.T) {
	raw := `{"v":1,"id":"m1","kind":"result","payload":{"cmd_id":"c1","ok":true,"duration_ms":1234}}`

	env, err := decodeEnvelope([]byte(raw))
	if err != nil {
		t.Fatalf("decodeEnvelope: %v", err)
	}

	result, err := decodePayload[ResultPayload](env)
	if err != nil {
		t.Fatalf("decodePayload: %v", err)
	}
	if result.DurationMS != 1234 {
		t.Errorf("DurationMS = %d, want 1234", result.DurationMS)
	}
	if !result.OK || result.CmdID != "c1" {
		t.Errorf("result = %+v, want ok=true cmd_id=c1", result)
	}
}
