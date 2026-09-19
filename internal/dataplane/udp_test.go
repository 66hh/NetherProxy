package dataplane

import (
	"testing"

	"github.com/pion/stun/v3"
)

func buildBindingRequest(t *testing.T, username string) []byte {
	t.Helper()
	attrs := []stun.Setter{stun.BindingRequest}
	if username != "" {
		attrs = append(attrs, stun.NewUsername(username))
	}
	msg := stun.MustBuild(attrs...)
	b, err := msg.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal STUN: %v", err)
	}
	return b
}

func TestParseSTUNServerUfrag(t *testing.T) {
	// 合法 binding request：USERNAME = "serverUfrag:clientUfrag"，取前半。
	ufrag, ok := parseSTUNServerUfrag(buildBindingRequest(t, "8F3k:clientufrag123"))
	if !ok || ufrag != "8F3k" {
		t.Errorf("ufrag = %q, ok = %v; want 8F3k, true", ufrag, ok)
	}

	// 非 STUN（如 DTLS record 首字节 0x16）应被拒绝。
	dtls := append([]byte{0x16, 0xfe, 0xfd, 0x00, 0x00}, make([]byte, 40)...)
	if _, ok := parseSTUNServerUfrag(dtls); ok {
		t.Error("DTLS 记录被误判为 STUN")
	}

	// 魔数正确但缺少 USERNAME。
	if _, ok := parseSTUNServerUfrag(buildBindingRequest(t, "")); ok {
		t.Error("缺少 USERNAME 的 STUN 被误判为可路由")
	}

	// USERNAME 无冒号分隔。
	if _, ok := parseSTUNServerUfrag(buildBindingRequest(t, "nocolon")); ok {
		t.Error("无冒号的 USERNAME 被误判为可路由")
	}

	// 过短的数据包。
	if _, ok := parseSTUNServerUfrag([]byte{0x00, 0x01, 0x00}); ok {
		t.Error("过短数据包被误判为 STUN")
	}
}
