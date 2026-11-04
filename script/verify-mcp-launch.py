#!/usr/bin/env python3
"""验证本仓编译出来的二进制真的能启动并列出工具。

为什么有这个脚本：MCP 运行时是按**路径**启动二进制的，而「原地覆盖」一个
已经在那个路径上的二进制，会让它下一次启动就起不来（被系统杀掉，没有
stderr）。实测：同一个路径第一次写进去能启动，被重建覆盖之后就起不来。

所以这个脚本在构建的最后验一次：起不来就换一个**新路径**再试，并把要改
指向的 `mcp update` 命令打出来。

用法：
    python3 script/verify-mcp-launch.py              # 验 ./bin/ 下的
    python3 script/verify-mcp-launch.py <目录>       # 验指定目录下的
"""
import json
import os
import shutil
import subprocess
import sys
import time

BIN_NAME = "jumpserver-exec"
# MCP 配置里那个 server 的名字。
# ⚠️ 可用 JMS_MCP_SERVER 覆盖：配置里的名字是**部署的事**，仓里不该钉死
# （部署可能把两个服务配成别的名字）。
SERVER_NAME = os.environ.get("JMS_MCP_SERVER", "jumpserver-exec")

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# ⚠️ 凭据不在本仓里：它从**运行环境**读，运行环境没有就从 MCP 运行时自己的
# 配置文件读（同一个地方，服务自己也是从那儿拿的）。环境变量优先 ——
# 显式给的 JUMPSERVER_TOKEN 是运维在说「用这一个」。
MCP_CONFIG = os.path.expanduser(
    os.environ.get("JMS_MCP_CONFIG", os.path.join("~", ".minimax", "mcp.json")))


def launches(path, env):
    """返回 (能不能起来, 工具数, 原因)。

    ⚠️ 「没答话」有**两种**原因：**起不来**（被系统杀掉）与**起来了但拒绝
    服务**（缺 JUMPSERVER_URL 之类，它自己会退出并说明原因）⇒ 两件事说成
    同一句「起不来」，会把人引到错的地方 ⇒ 失败时把它自己说的话带出来。
    """
    p = subprocess.Popen([path], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                         stderr=subprocess.PIPE, env=env, text=True, bufsize=1)
    try:
        p.stdin.write(json.dumps({
            "jsonrpc": "2.0", "id": 1, "method": "initialize",
            "params": {"protocolVersion": "2025-06-18", "capabilities": {},
                       "clientInfo": {"name": "build-check", "version": "1"}},
        }) + "\n")
        p.stdin.flush()
        line = p.stdout.readline()
        if not line:
            try:
                p.wait(timeout=3)
            except Exception:
                return False, 0, "不应答 initialize（进程还活着）"
            err = (p.stderr.read() or "").strip().splitlines()
            return False, 0, ("它退了：" + err[0]) if err else "它退了，但没有输出"
        p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": 2,
                                  "method": "tools/list", "params": {}}) + "\n")
        p.stdin.flush()
        tl = p.stdout.readline()
        tools = json.loads(tl).get("result", {}).get("tools", []) if tl else []
        return True, len(tools), ""
    except Exception as e:
        return False, 0, f"起不来：{e}"
    finally:
        p.kill()


def bastion_env():
    """服务运行时所在的环境。"""
    env = dict(os.environ)
    if not env.get("JUMPSERVER_TOKEN") or not env.get("JUMPSERVER_URL"):
        try:
            cfg = json.load(open(MCP_CONFIG))
            servers = cfg.get("mcpServers") or cfg.get("servers") or {}
            srv = servers[SERVER_NAME]
            srv_env = (srv.get("env") or {})
            token = srv_env.get("JUMPSERVER_TOKEN", "")
            if token and not env.get("JUMPSERVER_TOKEN"):
                env["JUMPSERVER_TOKEN"] = token
            url = srv_env.get("JUMPSERVER_URL", "")
            if url and not env.get("JUMPSERVER_URL"):
                env["JUMPSERVER_URL"] = url
        except Exception as e:
            # 不是吞掉：缺了 token，服务就会**没有凭据**地启动，而症状
            # （一个不答话的二进制）离原因十万八千里。
            print(f"could not read the credentials for {SERVER_NAME} from "
                  f"{MCP_CONFIG}: {e}", file=sys.stderr)
            print(f"  (用 JMS_MCP_SERVER 指定配置里那个 server 的名字；"
                  f"或直接给 JUMPSERVER_TOKEN / JUMPSERVER_URL)",
                  file=sys.stderr)
    return env


def main():
    bindir = sys.argv[1] if len(sys.argv) > 1 else os.path.join(ROOT, "bin")
    env = bastion_env()
    stamp = time.strftime("%Y%m%d-%H%M%S")
    # 兜底目录：放**新路径**上的那份，不是覆盖出来的。
    fallback_root = os.path.expanduser(
        os.environ.get("JMS_RUNTIME_ROOT",
                       os.path.join("~", ".local", "jumpserver-exec-runtime")))
    fallback_dir = os.path.join(fallback_root, stamp)
    moved = []

    path = os.path.join(bindir, BIN_NAME)
    if not os.path.exists(path):
        print(f"    FAIL {BIN_NAME}: {path} 不存在 —— 先跑 ./script/build.sh")
        return 1

    ok, n, why = launches(path, env)
    if ok:
        print(f"    OK   {BIN_NAME}: {n} tools")
        return 0

    os.makedirs(fallback_dir, exist_ok=True)
    alt = os.path.join(fallback_dir, BIN_NAME)
    shutil.copy(path, alt)
    os.chmod(alt, 0o755)
    ok2, n2, why2 = launches(alt, env)
    if ok2:
        moved.append((BIN_NAME, alt))
        print(f"    MOVED {BIN_NAME}: 覆盖把它弄坏了；能跑的那份在 {alt}")
    else:
        print(f"    FAIL {BIN_NAME}: 从任何路径都起不来 —— {why2}")
        print(f"    （第一次试：{why}）")
        return 1

    print()
    print("    这个二进制被覆盖过，失去了执行权限。把 MCP 配置指向新的一份：")
    for name, alt in moved:
        print(f"      mcp update name={SERVER_NAME} command={alt}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
