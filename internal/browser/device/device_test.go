package device

import (
	"strconv"
	"testing"
	"time"
)

func TestGenerateCodeIsSixDigits(t *testing.T) {
	seen := make(map[string]int)

	for range 200 {
		code, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode: %v", err)
		}
		if len(code) != 6 {
			t.Fatalf("配对码 %q 长度为 %d，插件侧按 6 位校验", code, len(code))
		}
		// 必须是纯数字且保留前导零——用户是手工输入的，"0042" 被截成 "42"
		// 会导致永远配对不上。
		if _, err := strconv.Atoi(code); err != nil {
			t.Fatalf("配对码 %q 不是纯数字", code)
		}
		seen[code]++
	}

	// 200 次抽样里出现大量重复说明随机源有问题（例如误用了固定种子）。
	if len(seen) < 190 {
		t.Errorf("200 次生成只得到 %d 个不同的配对码，随机性不足", len(seen))
	}
}

func TestGenerateTokenIsUniqueAndLong(t *testing.T) {
	seen := make(map[string]struct{})

	for range 100 {
		token, err := GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		// 256 bit 经 base64url（无填充）编码后是 43 个字符。
		if len(token) < 40 {
			t.Fatalf("token %q 太短（%d 字符），熵不足", token, len(token))
		}
		if _, dup := seen[token]; dup {
			t.Fatalf("生成了重复的 token")
		}
		seen[token] = struct{}{}
	}
}

func TestHashTokenIsStableAndHidesInput(t *testing.T) {
	const token = "some-device-token"

	first := HashToken(token)
	if first != HashToken(token) {
		t.Error("HashToken 对同一输入必须稳定，否则查表永远查不到")
	}
	if len(first) != 64 {
		t.Errorf("SHA-256 十六进制串应为 64 字符，得到 %d", len(first))
	}
	if first == token {
		t.Error("哈希结果不能等于明文")
	}
	if HashToken("some-device-tokeN") == first {
		t.Error("不同输入产生了相同哈希")
	}
}

func TestConstantTimeEqual(t *testing.T) {
	if !ConstantTimeEqual("abc", "abc") {
		t.Error("相同字符串应判定相等")
	}
	if ConstantTimeEqual("abc", "abd") {
		t.Error("不同字符串应判定不等")
	}
	if ConstantTimeEqual("abc", "abcd") {
		t.Error("长度不同应判定不等")
	}
}

func TestDeviceRevokedAndExpired(t *testing.T) {
	now := time.Now()

	active := Device{ExpiresAt: now.Add(time.Hour)}
	if active.Revoked() {
		t.Error("未设置 RevokedAt 的设备不该判定为已吊销")
	}
	if active.Expired(now) {
		t.Error("有效期内的设备不该判定为已过期")
	}

	revokedAt := now.Add(-time.Minute)
	revoked := Device{ExpiresAt: now.Add(time.Hour), RevokedAt: &revokedAt}
	if !revoked.Revoked() {
		t.Error("设置了 RevokedAt 的设备应判定为已吊销")
	}

	expired := Device{ExpiresAt: now.Add(-time.Second)}
	if !expired.Expired(now) {
		t.Error("过期设备应判定为已过期")
	}
}
