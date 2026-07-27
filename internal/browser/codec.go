package browser

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// rawEnvelope 是解码用的信封：Payload 保持未解析状态，等确定 kind 之后再
// 按对应类型解析。直接用 map[string]any 会丢失数字类型（JSON 数字全变
// float64），tab_id 这类字段会出问题。
type rawEnvelope struct {
	V       int             `json:"v"`
	ID      string          `json:"id"`
	TS      string          `json:"ts"`
	Kind    MessageKind     `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// newEnvelope 构造一条待发送的消息。
func newEnvelope(kind MessageKind, payload any) Envelope {
	return Envelope{
		V:       ProtocolVersion,
		ID:      uuid.NewString(),
		TS:      nowTS(),
		Kind:    kind,
		Payload: payload,
	}
}

// marshalEnvelope 序列化一条待发送的消息。
func marshalEnvelope(env Envelope) ([]byte, error) {
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal %s envelope: %w", env.Kind, err)
	}
	return data, nil
}

// decodeEnvelope 解析插件发来的原始帧。
//
// **不信任任何字段**——插件可能是旧版本、被篡改的构建，或者根本不是我们的
// 插件。解析失败返回 error 由调用方丢弃该帧并记 WARN，绝不 panic，也绝不
// 因为一个坏帧就断开整条连接（否则一个畸形帧就能让设备反复重连）。
func decodeEnvelope(data []byte) (rawEnvelope, error) {
	var env rawEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return rawEnvelope{}, fmt.Errorf("unmarshal envelope: %w", err)
	}
	if env.Kind == "" {
		return rawEnvelope{}, fmt.Errorf("envelope missing kind")
	}
	if env.ID == "" {
		return rawEnvelope{}, fmt.Errorf("envelope missing id")
	}
	return env, nil
}

// decodePayload 把信封载荷解析成具体类型。
func decodePayload[T any](env rawEnvelope) (T, error) {
	var out T
	if len(env.Payload) == 0 {
		return out, fmt.Errorf("envelope %s has empty payload", env.Kind)
	}
	if err := json.Unmarshal(env.Payload, &out); err != nil {
		return out, fmt.Errorf("unmarshal %s payload: %w", env.Kind, err)
	}
	return out, nil
}
