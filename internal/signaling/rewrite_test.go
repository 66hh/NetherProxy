package signaling

import (
	"net/netip"
	"strings"
	"testing"
)

// 文档附录 A 的 SDP 重写对照示例。
const sampleAnswer = `v=0
o=- 123456 2 IN IP4 127.0.0.1
s=-
t=0 0
a=group:BUNDLE 0
a=msid-semantic: WMS
a=identity:eyJmb28iOiJiYXIifQ==
m=application 9 UDP/DTLS/SCTP webrtc-datachannel
c=IN IP4 0.0.0.0
a=candidate:1 1 udp 2122260223 192.168.1.5 40001 typ host generation 0 ufrag 8F3k network-id 1 network-cost 0
a=candidate:2 1 udp 2122194687 127.0.0.1 40001 typ host generation 0 ufrag 8F3k network-id 2 network-cost 0
a=candidate:3 1 udp 1686052607 203.0.113.7 40001 typ srflx raddr 192.168.1.5 rport 40001 generation 0 ufrag 8F3k network-id 3 network-cost 0
a=ice-ufrag:8F3k
a=ice-pwd:secretpwd0123456789abcd
a=ice-options:trickle
a=fingerprint:sha-256 00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF
a=setup:passive
a=mid:0
a=sctp-port:5000
a=max-message-size:262144
`

func TestRewriteAnswer(t *testing.T) {
	ip := netip.MustParseAddr("203.0.113.7")
	res, err := RewriteAnswer([]byte(sampleAnswer), ip, 19133)
	if err != nil {
		t.Fatalf("RewriteAnswer: %v", err)
	}

	if res.Ufrag != "8F3k" {
		t.Errorf("ufrag = %q, want 8F3k", res.Ufrag)
	}
	wantBackend := netip.MustParseAddrPort("192.168.1.5:40001")
	if res.BackendAddr != wantBackend {
		t.Errorf("backend = %v, want %v", res.BackendAddr, wantBackend)
	}

	out := string(res.Body)

	// candidate 全量替换为一条指向代理公网地址的 host candidate。
	if n := strings.Count(out, "a=candidate:"); n != 1 {
		t.Errorf("candidate 行数 = %d, want 1\n%s", n, out)
	}
	if !strings.Contains(out, "a=candidate:1 1 udp 2122260223 203.0.113.7 19133 typ host") {
		t.Errorf("缺少指向代理的 candidate\n%s", out)
	}
	if strings.Contains(out, "192.168.1.5") || strings.Contains(out, "typ srflx") {
		t.Errorf("原始 candidate 泄漏\n%s", out)
	}

	// c= 行重写。
	if !strings.Contains(out, "c=IN IP4 203.0.113.7") {
		t.Errorf("c= 行未重写\n%s", out)
	}

	// 以下字段必须逐字节保持：ufrag / pwd / fingerprint / identity（端到端）。
	for _, keep := range []string{
		"a=ice-ufrag:8F3k",
		"a=ice-pwd:secretpwd0123456789abcd",
		"a=fingerprint:sha-256 00:11:22:33",
		"a=identity:eyJmb28iOiJiYXIifQ==",
		"a=setup:passive",
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("应保持原样的字段缺失: %q\n%s", keep, out)
		}
	}

	// m= 行端口必须重写为公网端口（BDS 的 m= 是真实数据端口）。
	if !strings.Contains(out, "m=application 19133 UDP/DTLS/SCTP webrtc-datachannel") {
		t.Errorf("m= 行端口未重写\n%s", out)
	}
}

func TestRewriteAnswerOnlyLoopback(t *testing.T) {
	// 代理与 BDS 同机：answer 只有回环 candidate 时应接受。
	answer := "v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\n" +
		"m=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=candidate:1 1 udp 2122194687 127.0.0.1 40001 typ host generation 0 ufrag ab network-id 1 network-cost 0\r\n" +
		"a=ice-ufrag:ab\r\na=ice-pwd:pwd0123456789012345678\r\n" +
		"a=fingerprint:sha-256 00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF\r\n" +
		"a=setup:passive\r\na=mid:0\r\na=sctp-port:5000\r\na=max-message-size:262144\r\n"
	res, err := RewriteAnswer([]byte(answer), netip.MustParseAddr("10.0.0.1"), 19133)
	if err != nil {
		t.Fatalf("RewriteAnswer: %v", err)
	}
	if res.BackendAddr != netip.MustParseAddrPort("127.0.0.1:40001") {
		t.Errorf("backend = %v, want 127.0.0.1:40001", res.BackendAddr)
	}
	// CRLF 行结束符应保持。
	if !strings.Contains(string(res.Body), "\r\n") {
		t.Error("CRLF 行结束符未保留")
	}
}

func TestRewriteAnswerMissingUfrag(t *testing.T) {
	_, err := RewriteAnswer([]byte("v=0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n"), netip.MustParseAddr("10.0.0.1"), 19133)
	if err == nil {
		t.Fatal("缺少 ice-ufrag 时应返回错误")
	}
}

func TestRewriteAnswerNoCandidate(t *testing.T) {
	_, err := RewriteAnswer([]byte("v=0\r\na=ice-ufrag:ab\r\n"), netip.MustParseAddr("10.0.0.1"), 19133)
	if err == nil {
		t.Fatal("无 candidate 时应返回错误")
	}
}

func TestRewriteOffer(t *testing.T) {
	offer := "v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\n" +
		"m=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=candidate:aa 1 udp 2122260223 192.168.0.50 51234 typ host generation 0 ufrag cliUfrag network-id 1 network-cost 0\r\n" +
		"a=candidate:bb 1 udp 2122194687 127.0.0.1 51234 typ host generation 0 ufrag cliUfrag network-id 2 network-cost 0\r\n" +
		"a=ice-ufrag:cliUfrag\r\na=ice-pwd:clipwd012345678901234567\r\n" +
		"a=fingerprint:sha-256 00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF\r\n" +
		"a=setup:actpass\r\na=mid:0\r\na=sctp-port:5000\r\na=max-message-size:262144\r\n"
	out, err := RewriteOffer([]byte(offer))
	if err != nil {
		t.Fatalf("RewriteOffer: %v", err)
	}
	s := string(out)
	if n := strings.Count(s, "a=candidate:"); n != 1 {
		t.Errorf("candidate 行数 = %d, want 1\n%s", n, s)
	}
	if !strings.Contains(s, "a=candidate:1 1 udp 2122260223 192.0.2.1 9 typ host generation 0 ufrag cliUfrag") {
		t.Errorf("占位 candidate 缺失或 ufrag 错误\n%s", s)
	}
	if strings.Contains(s, "192.168.0.50") || strings.Contains(s, "127.0.0.1 51234") {
		t.Errorf("客户端真实 candidate 泄漏\n%s", s)
	}
	for _, keep := range []string{"a=ice-ufrag:cliUfrag", "a=fingerprint:sha-256 00:11", "a=setup:actpass"} {
		if !strings.Contains(s, keep) {
			t.Errorf("应保持原样的字段缺失: %q\n%s", keep, s)
		}
	}
}

func TestRewriteOfferMissingUfrag(t *testing.T) {
	if _, err := RewriteOffer([]byte("v=0\r\n")); err == nil {
		t.Fatal("缺少 ice-ufrag 时应返回错误")
	}
}
