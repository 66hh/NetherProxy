"""端到端功能测试编排: 热更代理配置 (关 JWT 验证、BDS 指向 fake), 跑客户端用例, 恢复现场。

前置: 代理 (netherproxy.exe) 已在运行, config.yml 在同目录。
用法: python tests/run_test.py
退出码: 0 通过, 1 失败
"""
import json
import re
import subprocess
import sys
import time
import urllib.request

GATEWAY = "http://127.0.0.1:19130"
CONFIG_PATH = "config.yml"


def read_token() -> str:
    with open(CONFIG_PATH, encoding="utf-8") as f:
        m = re.search(r"^\s*token:\s*(\S+)", f.read(), re.M)
    if not m:
        sys.exit("config.yml 中找不到 gateway.token")
    return m.group(1)


def api(method, path, token, body=None):
    req = urllib.request.Request(
        GATEWAY + path, method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.load(resp)


def main():
    token = read_token()

    # 备份当前配置
    original = api("GET", "/api/config", token)

    bds_out = open("tests/fake_bds.out", "w")
    bds = subprocess.Popen(
        [sys.executable, "tests/fake_bds.py"],
        stdout=bds_out, stderr=subprocess.STDOUT)
    time.sleep(1)

    try:
        # 热更: 关 JWT 验证, BDS 指向 fake
        cfg = json.loads(json.dumps(original))
        cfg["gateway"]["verify_identity"] = False
        cfg["bds"] = [{
            "enable": True, "domain": "*",
            "host": "127.0.0.1", "port": 19501,
            "heartbeat": {"enable": False, "manual_only": False, "interval": "5s", "timeout": "2s", "retries": 3},
        }]
        api("PUT", "/api/config", token, cfg)
        print("config switched to fake bds (verify_identity=false)", flush=True)

        result = subprocess.run([sys.executable, "tests/fake_client.py"])
        code = result.returncode
    finally:
        # 恢复现场
        try:
            api("PUT", "/api/config", token, original)
            print("config restored", flush=True)
        except Exception as e:
            print(f"恢复配置失败, 请检查 config.yml: {e}", file=sys.stderr)
        bds.terminate()
        bds_out.close()

    sys.exit(code)


if __name__ == "__main__":
    main()
