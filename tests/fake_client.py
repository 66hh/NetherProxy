"""模拟 Minecraft 客户端: 完整 join 流程 + 数据面验证。

前置: 代理运行中且 verify_identity=false, fake BDS 已启动并被代理路由。
用法: python tests/fake_client.py
退出码: 0 全部通过, 1 有用例失败
"""
import base64
import hashlib
import hmac
import json
import os
import socket
import struct
import sys
import urllib.request

# 测试线路地址 (由 run_test.py 经环境变量注入, 对应 multiplexer 监听地址)
ENTRY_HOST = os.environ.get("NP_ENTRY_HOST", "127.0.0.1")
ENTRY_PORT = int(os.environ.get("NP_ENTRY_PORT", "19131"))

GATEWAY = "http://127.0.0.1:19130"

# 测试玩家信息 (虚构, 非真实账号)
TEST_PLAYER_NAME = "TestPlayer"
TEST_PLAYER_XUID = "0000000000000000"

failures = []


def check(name, ok, detail=""):
    print(f"{'PASS' if ok else 'FAIL'}: {name} {detail}", flush=True)
    if not ok:
        failures.append(name)


def b64u(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def make_identity() -> str:
    """构造结构合法的假 identity (代理关闭验签时仅解析 xname/xid)。"""
    header = b64u(json.dumps({"alg": "RS256", "typ": "JWT", "kid": "TEST"}).encode())
    payload = b64u(json.dumps({"xname": TEST_PLAYER_NAME, "xid": TEST_PLAYER_XUID}).encode())
    token = f"{header}.{payload}.fakesig"
    assertion = json.dumps({"fingerprints": f"{b64u(b'x')}.{b64u(b'y')}.z", "token": token})
    return b64u(json.dumps({"assertion": assertion, "idp": {"domain": "self", "protocol": "default"}}).encode())


def make_offer() -> bytes:
    return (
        "v=0\r\n"
        "o=- 1 1 IN IP4 127.0.0.1\r\n"
        "s=-\r\n"
        "t=0 0\r\n"
        "a=group:BUNDLE 0\r\n"
        f"a=identity:{make_identity()}\r\n"
        "m=application 50000 UDP/DTLS/SCTP webrtc-datachannel\r\n"
        "c=IN IP4 127.0.0.1\r\n"
        "a=candidate:2000000001 1 udp 2122129151 127.0.0.1 50000 typ host generation 0 network-id 1 network-cost 10\r\n"
        "a=ice-ufrag:clientuf\r\n"
        "a=ice-pwd:clientpwd123456789012345\r\n"
        "a=fingerprint:sha-256 FF:EE:DD:CC:BB:AA:99:88:77:66:55:44:33:22:11:00"
        ":FF:EE:DD:CC:BB:AA:99:88:77:66:55:44:33:22:11:00\r\n"
        "a=setup:actpass\r\n"
        "a=mid:0\r\n"
        "a=sctp-port:5000\r\n"
    ).encode()


def pad4(b: bytes) -> bytes:
    return b + b"\x00" * ((4 - len(b) % 4) % 4)


def build_stun(username: str, pwd: str | None, bad_mac=False) -> bytes:
    txid = os.urandom(12)
    uattr = struct.pack(">HH", 0x0006, len(username)) + pad4(username.encode())
    if pwd is None:
        header = struct.pack(">HHI", 0x0001, len(uattr), 0x2112A442) + txid
        return header + uattr
    header = struct.pack(">HHI", 0x0001, len(uattr) + 24, 0x2112A442) + txid
    mac = hmac.new(pwd.encode(), header + uattr, hashlib.sha1).digest()
    if bad_mac:
        mac = bytes([mac[0] ^ 1]) + mac[1:]
    return header + uattr + struct.pack(">HH", 0x0008, 20) + mac


def send_udp(payload: bytes, addr, timeout=2.0):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(timeout)
    s.sendto(payload, addr)
    try:
        data, _ = s.recvfrom(4096)
        return data
    except socket.timeout:
        return None
    finally:
        s.close()


def parse_answer(sdp: str):
    ufrag = pwd = None
    candidate = None
    for line in sdp.replace("\r\n", "\n").split("\n"):
        if line.startswith("a=ice-ufrag:"):
            ufrag = line.split(":", 1)[1].strip()
        elif line.startswith("a=ice-pwd:"):
            pwd = line.split(":", 1)[1].strip()
        elif line.startswith("a=candidate:"):
            f = line.split()
            candidate = (f[4], int(f[5]))
    return ufrag, pwd, candidate


def main():
    # 1. MOTD 透传
    motd = json.load(urllib.request.urlopen(f"{GATEWAY}/v1/join", timeout=5))
    check("motd", motd.get("name") == "fake-bds", f"got {motd.get('name')!r}")

    # 2. POST offer, answer 的 candidate 必须被重写为代理 entry
    req = urllib.request.Request(
        f"{GATEWAY}/v1/join/12345678901234567890", data=make_offer(),
        headers={"Content-Type": "application/sdp"}, method="POST")
    answer = urllib.request.urlopen(req, timeout=10).read().decode()
    check("answer is sdp", answer.lstrip().startswith("v="))
    ufrag, pwd, candidate = parse_answer(answer)
    check("answer has ufrag", ufrag is not None and ufrag.startswith("fakeuf"), f"ufrag={ufrag}")
    check("answer has pwd", pwd is not None)
    check("candidate rewritten to entry", candidate == (ENTRY_HOST, ENTRY_PORT),
          f"candidate={candidate}, expect=({ENTRY_HOST}, {ENTRY_PORT})")

    if not (ufrag and pwd and candidate):
        print("answer 解析失败, 中止", flush=True)
        sys.exit(1)

    # 3. 带正确 MESSAGE-INTEGRITY 的 STUN -> 经代理转发并收到 echo
    stun = build_stun(f"{ufrag}:clientuf", pwd)
    resp = send_udp(stun, candidate)
    check("stun with valid MI forwarded", resp == stun)

    # 4. 同一源地址发非 STUN 数据 (模拟 DTLS) -> 按 5-tuple 转发
    # 注意: 需要与步骤 3 同一 socket (同一 5-tuple)
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(2)
    s.sendto(stun, candidate)  # 先学习地址
    s.recvfrom(4096)
    dtls_like = b"\x16\xfe\xfd" + os.urandom(32)
    s.sendto(dtls_like, candidate)
    try:
        resp, _ = s.recvfrom(4096)
        check("non-stun data forwarded by 5-tuple", resp == dtls_like)
    except socket.timeout:
        check("non-stun data forwarded by 5-tuple", False, "timeout")
    s.close()

    # 5. 错误 HMAC 的 STUN 应被丢弃
    resp = send_udp(build_stun(f"{ufrag}:clientuf", pwd, bad_mac=True), candidate)
    check("stun with bad MI dropped", resp is None)

    # 6. 未知 ufrag 的 STUN 应被丢弃
    resp = send_udp(build_stun("nosuchuf:clientuf", pwd), candidate)
    check("stun with unknown ufrag dropped", resp is None)

    print(f"\n{'全部通过' if not failures else f'{len(failures)} 项失败: ' + ', '.join(failures)}", flush=True)
    sys.exit(0 if not failures else 1)


if __name__ == "__main__":
    main()
