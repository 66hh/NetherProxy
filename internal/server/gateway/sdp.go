package gateway

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// RewriteResult 携带 answer 重写结果与建立会话所需的信息。
type RewriteResult struct {
	// Body 是重写后的 SDP answer。
	Body []byte
	// Ufrag 是服务端 ice-ufrag（路由键，保持原样）。
	Ufrag string
	// Pwd 是服务端 ice-pwd, 用于数据面校验客户端 STUN 的 MESSAGE-INTEGRITY。
	Pwd string
	// BackendAddr 是 BDS 为该连接分配的 UDP 地址（原始 host candidate）。
	BackendAddr netip.AddrPort
}

// RewriteAnswer 对 SDP answer 做行级重写:
//
//  1. 提取 a=ice-ufrag（保持原样，作为数据面路由键）与 a=ice-pwd（完整性校验密钥）；
//  2. 从原始 a=candidate 中提取后端地址（优先 UDP host candidate）；
//  3. 删除全部原始 candidate，替换为一条指向线路公网地址的 candidate；
//  4. m= 行端口与 c= 连接行地址同步替换为线路公网地址；
//  5. a=fingerprint / a=identity / a=ice-ufrag / a=ice-pwd 等一律原样保留
//     （DTLS 端到端与身份绑定不经过代理）。
//
// 采用行级文本重写而非整体反序列化/再序列化，保证除目标字段外逐字节不变，
// 最大程度兼容原版客户端的 SDP 解析器。
func RewriteAnswer(body []byte, advertiseIP netip.Addr, publicPort uint16) (*RewriteResult, error) {
	text := string(body)
	crlf := strings.Contains(text, "\r\n")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	// 第一遍：提取 ufrag/pwd、后端地址与候选模板行。candidate 行在 SDP 中
	// 可能出现在 ice-ufrag 之前（pion 的输出顺序如此），因此需要两遍扫描。
	// BDS 通常携带多条 host candidate（回环 + 内网网卡），优先选择
	// 非回环地址；仅有回环地址时退而用之（代理与 BDS 同机的场景）。
	var ufrag, pwd string
	var backendAddr, loopbackAddr netip.AddrPort
	var templateLine, loopbackLine string
	haveBackend, haveLoopback := false, false
	for _, line := range lines {
		if v, ok := strings.CutPrefix(line, "a=ice-ufrag:"); ok {
			ufrag = strings.TrimSpace(v)
			continue
		}
		if v, ok := strings.CutPrefix(line, "a=ice-pwd:"); ok {
			pwd = strings.TrimSpace(v)
			continue
		}
		addr, ok := parseCandidateBackend(line)
		if !ok {
			continue
		}
		if addr.Addr().IsLoopback() {
			if !haveLoopback {
				loopbackAddr, loopbackLine = addr, line
				haveLoopback = true
			}
		} else if !haveBackend {
			backendAddr, templateLine = addr, line
			haveBackend = true
		}
	}
	if ufrag == "" {
		return nil, errors.New("missing ice-ufrag in answer")
	}
	if !haveBackend {
		if !haveLoopback {
			return nil, errors.New("no usable UDP candidate in answer")
		}
		backendAddr, templateLine = loopbackAddr, loopbackLine
	}

	// 第二遍：重写 candidate、m= 端口与 c= 行。
	out := make([]string, 0, len(lines)+1)
	candidateInserted := false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "a=candidate:"):
			// 删除全部原始 candidate，仅在第一条的位置插入重写后的候选:
			// 以原版候选行为模板只替换地址字段, 保持与原版一致的格式
			// (foundation/priority/network-cost 等)。
			if !candidateInserted {
				out = append(out, rewriteCandidateAddr(templateLine, advertiseIP, publicPort))
				candidateInserted = true
			}
		case strings.HasPrefix(line, "m="):
			// BDS 的 m= 行端口是真实数据端口（非占位符 9），必须同步重写为
			// 公网端口，与 candidate 保持一致。
			out = append(out, rewriteMLine(line, publicPort))
		case strings.HasPrefix(line, "c="):
			out = append(out, rewriteCLine(line, advertiseIP))
		default:
			out = append(out, line)
		}
	}

	sep := "\n"
	if crlf {
		sep = "\r\n"
	}
	return &RewriteResult{
		Body:        []byte(strings.Join(out, sep)),
		Ufrag:       ufrag,
		Pwd:         pwd,
		BackendAddr: backendAddr,
	}, nil
}

// rewriteMLine 重写 m= 行端口为公网端口，保留其余字段。
// 行格式：m=application 19137 UDP/DTLS/SCTP webrtc-datachannel
func rewriteMLine(line string, port uint16) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return line
	}
	fields[1] = strconv.Itoa(int(port))
	return strings.Join(fields, " ")
}

// parseCandidateBackend 从 a=candidate 行解析后端 UDP 地址。
// 仅接受 UDP 协议、host 类型的 candidate（BDS 真实监听地址）；
// srflx/relay 地址对同内网代理无意义。
//
// 行格式：a=candidate:{foundation} {component} udp {priority} {ip} {port} typ {type} ...
func parseCandidateBackend(line string) (netip.AddrPort, bool) {
	if !strings.HasPrefix(line, "a=candidate:") {
		return netip.AddrPort{}, false
	}
	fields := strings.Fields(line)
	if len(fields) < 8 {
		return netip.AddrPort{}, false
	}
	if !strings.EqualFold(fields[2], "udp") {
		return netip.AddrPort{}, false
	}
	if fields[6] != "typ" || !strings.EqualFold(fields[7], "host") {
		return netip.AddrPort{}, false
	}
	ip, err := netip.ParseAddr(fields[4])
	if err != nil {
		return netip.AddrPort{}, false
	}
	port, err := strconv.ParseUint(fields[5], 10, 16)
	if err != nil || port == 0 {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(ip, uint16(port)), true
}

// RewriteOffer 重写 SDP offer（relay_only 中继模式）：
// 删除客户端的全部 candidate，替换为一条不可达占位地址（192.0.2.1:9，TEST-NET-1）。
//
// 用途：
//   - 隐藏客户端真实地址：BDS 无法从 offer 中获取玩家 IP
//   - 同网段部署时防止 BDS 对客户端 candidate 反向建连旁路代理
//
// 替换后 BDS 无法主动出击，只能等待客户端 check 经代理到达，
// 通过 ICE peer-reflexive 机制建立连接，全部流量必经代理。
// 公网部署无需开启：公网客户端的私有地址对 BDS 天然不可达。
func RewriteOffer(body []byte) ([]byte, error) {
	text := string(body)
	crlf := strings.Contains(text, "\r\n")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	var ufrag string
	for _, line := range lines {
		if v, ok := strings.CutPrefix(line, "a=ice-ufrag:"); ok {
			ufrag = strings.TrimSpace(v)
		}
	}
	if ufrag == "" {
		return nil, errors.New("missing ice-ufrag in offer")
	}

	out := make([]string, 0, len(lines)+1)
	replaced := false
	for _, line := range lines {
		if strings.HasPrefix(line, "a=candidate:") {
			// 在第一条 candidate 的位置插入占位，其余删除。
			if !replaced {
				out = append(out, fmt.Sprintf("a=candidate:1 1 udp 2122260223 192.0.2.1 9 typ host generation 0 ufrag %s network-id 1 network-cost 0", ufrag))
				replaced = true
			}
			continue
		}
		out = append(out, line)
	}

	sep := "\n"
	if crlf {
		sep = "\r\n"
	}
	return []byte(strings.Join(out, sep)), nil
}

// rewriteCandidateAddr 以模板 candidate 行为基础, 仅替换 IP 与端口字段
// 行格式：a=candidate:{foundation} {component} udp {priority} {ip} {port} typ {type} ...
func rewriteCandidateAddr(line string, ip netip.Addr, port uint16) string {
	fields := strings.Fields(line)
	if len(fields) < 6 {
		return line
	}
	fields[4] = ip.String()
	fields[5] = strconv.Itoa(int(port))
	return strings.Join(fields, " ")
}

// rewriteCLine 重写 c= 连接行地址，保留网络类型与地址族。
func rewriteCLine(line string, ip netip.Addr) string {
	// 行格式：c=IN IP4 0.0.0.0
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return line
	}
	addrType := "IP4"
	if ip.Is6() {
		addrType = "IP6"
	}
	return fields[0] + " " + addrType + " " + ip.String()
}
