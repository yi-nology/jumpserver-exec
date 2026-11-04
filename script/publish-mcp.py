#!/usr/bin/env python3
"""把本仓编译出来的二进制发布到一个稳定的运行时目录。

为什么有这个脚本：MCP 运行时是按**路径**启动二进制的，而「原地覆盖」一个
已经在那个路径上的二进制，会让它下一次启动就起不来（被系统杀掉，没有
stderr）。所以每次发布都落在一个**全新的、带时间戳的目录**里，配置文件指向
那个目录即可 —— 旧的还留着，回滚只要把指针改回去。

用法：
    ./script/build.sh && python3 script/publish-mcp.py
    python3 script/publish-mcp.py <二进制路径>     # 默认取 ./bin/<BIN_NAME>

发布到哪里：
    ~/.local/<SERVER_NAME>-runtime/<时间戳>/<BIN_NAME>
    （可用 JMS_RUNTIME_ROOT 改；旧目录会保留最近几份，配置当前指向的那份
      永远不删 —— 回滚要靠它。）

打印出来的 `mcp update` 命令就是让 MCP 运行时改指向哪里的那条。
"""
import glob
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

# 仓根由脚本自己的位置推出来 ⇒ 整个目录可以随便改名或搬走，不用改路径。
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# 运行时目录：每个仓一个（管理面与执行面是**两个** MCP 服务，两条发布线）。
RUNTIME_ROOT = os.path.expanduser(
    os.environ.get("JMS_RUNTIME_ROOT",
                    os.path.join("~", ".local", "jumpserver-exec-runtime")))

# ⚠️ 凭据不在本仓里：它从**运行环境**读，运行环境没有就从 MCP 运行时自己的
# 配置文件读（同一个地方，服务自己也是从那儿拿的）。环境变量优先 ——
# 显式给的 JUMPSERVER_TOKEN 是运维在说「用这一个」。
#
# ⚠️ JUMPSERVER_URL 在这里**也没有兜底**：它以前默认写死过一个地址，于是这个
# 脚本成了最后往部署配置里写地址的地方 —— 地址换掉之后，那个值就指着空气
# 了。地址要么来自环境，要么来自同一份 MCP 配置；两处都没有时，服务自己会
# 说（"JUMPSERVER_URL is required"），而不是被一个脚本指着别处。
MCP_CONFIG = os.path.expanduser(
    os.environ.get("JMS_MCP_CONFIG", os.path.join("~", ".minimax", "mcp.json")))


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


def launches(path, env):
    """二进制能不能起来并应答 initialize。返回 (ok, 原因)。

    ⚠️ 「没答话」有**两种**原因：**起不来**（被系统杀掉）与**起来了但拒绝
    服务**（缺 JUMPSERVER_URL 之类，它自己会退出并说明原因）⇒ 两件事说成
    同一句「起不来」，会把人引到错的地方 ⇒ 失败时把它自己说的话带出来。
    """
    p = subprocess.Popen([path], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                         stderr=subprocess.PIPE, env=env, text=True, bufsize=1)
    try:
        p.stdin.write('{"jsonrpc":"2.0","id":1,"method":"initialize","params":'
                      '{"protocolVersion":"2025-06-18","capabilities":{},'
                      '"clientInfo":{"name":"publish","version":"1"}}}\n')
        p.stdin.flush()
        if p.stdout.readline():
            return True, ""
        try:
            p.wait(timeout=3)
        except Exception:
            return False, "不应答 initialize（进程还活着）"
        err = (p.stderr.read() or "").strip().splitlines()
        if not err:
            return False, "它退了，但没有输出"
        text = "\n".join(err)
        if "panic:" in text or "goroutine " in text:
            return False, "它崩了：" + err[0]
        # 它自己说明了为什么不做 ⇒ 二进制起得来，缺的是配置。
        return True, "它按设计拒绝服务（缺配置）：" + err[0]
    except Exception as e:
        return False, f"起不来：{e}"
    finally:
        p.kill()


def provenance(path):
    """这个二进制是从哪儿来的：哪个 commit，以及是不是从一个**脏**工作树编的。

    为什么要在意："vcs.modified=true" 意味着二进制是从有未提交改动的工作树
    链出来的。它照样能跑，通常也带着你刚写的代码 —— 但它不是任何一个 commit
    的可追溯产物，而这两件事从外面看一模一样（同样的工具数、同样的行为）。
    ⇒ 读出出处、打印出来，工作树脏的时候**说出来**。
    """
    try:
        out = subprocess.run(["go", "version", "-m", path], capture_output=True,
                             text=True, timeout=60).stdout
    except Exception:
        return None, None
    rev = mod = None
    for line in out.splitlines():
        line = line.strip()
        if not line.startswith("build"):
            continue
        if "vcs.revision=" in line:
            rev = line.split("vcs.revision=")[1].split()[0]
        elif "vcs.modified=" in line:
            mod = line.split("vcs.modified=")[1].split()[0]
    return rev, mod


def main():
    env = bastion_env()
    src = sys.argv[1] if len(sys.argv) > 1 else os.path.join(ROOT, "bin", BIN_NAME)
    if not os.path.exists(src):
        print(f"    MISSING {BIN_NAME}: {src} 不存在 —— 先跑 ./script/build.sh")
        return 1
    print(f"  using build: {src}")

    # 发布到一个**新**路径：原地覆盖正是那个会失去执行权限的动作。
    stamp = time.strftime("%Y%m%d-%H%M%S")
    dest = os.path.join(os.path.expanduser(RUNTIME_ROOT), stamp)
    os.makedirs(dest, exist_ok=True)

    dst = os.path.join(dest, BIN_NAME)
    shutil.copy(src, dst)
    os.chmod(dst, 0o755)
    good, why = launches(dst, env)
    rev, mod = provenance(dst)
    tag = ""
    if rev:
        tag = f"  [{rev[:12]}{' +DIRTY' if mod == 'true' else ''}]"
    print(f"    {'OK  ' if good else 'FAIL'} {dst}   (from {src}){tag}")
    if why:
        # ⚠️ 「它按设计拒绝服务」也要说出来：OK 不等于「它应答了 initialize」。
        print(f"         {why}")

    if mod == "true":
        print()
        print("  ⚠️  这个二进制来自**脏**工作树（vcs.modified=true）：")
        print("     ⇒ 它不是任何一个 commit 的可追溯产物，")
        print("       尽管它通常带着你刚写的代码。")
        print("     ⇒ 先提交，再构建发布一次，得到的二进制才写着自己的 commit。")

    print()
    print("  point the MCP config at this:")
    print(f"    mcp update name={SERVER_NAME} command={dst}")

    prune_old_runtimes(dest)
    return 0 if good else 1


# ⚠️ 每次发布都留下一份几十 MB 的运行时副本 ⇒ 没有这一步，目录每轮长一份。
#
# ⚠️ 保留三样东西：最近的几份、**MCP 配置当前指向的那一份**（回滚要靠它）、
# 以及刚刚发布的这一份 ⇒ 删的都是**没人指向**的旧副本。
KEEP_RUNTIMES = 3


def prune_old_runtimes(keep):
    root = os.path.expanduser(RUNTIME_ROOT)
    if not os.path.isdir(root):
        return
    stamps = sorted(d for d in os.listdir(root)
                    if os.path.isdir(os.path.join(root, d)))
    stale = set(stamps[:-KEEP_RUNTIMES]) - {os.path.basename(keep)}
    # 配置指着的那个留着，哪怕它更老。
    #
    # ⚠️ 配置**读不到**时，这一轮整个跳过：配置是唯一知道哪一份在用的东西，
    # 而「发布」和「mcp update」之间的那段时间里，配置指的正是**旧**的那份
    # ⇒ 删了它，MCP 服务就没东西可启动了。
    pointed, err = configured_commands()
    if err:
        print(f"    NOT pruning old runtimes: {err}")
        return
    for path in pointed:
        stale.discard(os.path.basename(os.path.dirname(os.path.realpath(path))))
    for d in sorted(stale):
        p = os.path.join(root, d)
        # ⚠️ 删的是**可再生的构建产物**（不是留痕、不是配置、不是源码）⇒ 永久删除。
        shutil.rmtree(p, ignore_errors=True)
    if stale:
        print(f"    pruned {len(stale)} old runtime dir(s), kept the last {KEEP_RUNTIMES}")


def configured_commands():
    """MCP 配置当前指向的那些二进制。"""
    out = []
    try:
        servers = json.load(open(MCP_CONFIG)).get("mcpServers") or {}
    except Exception as e:
        return out, f"cannot read {MCP_CONFIG}: {e}"
    for srv in servers.values():
        cmd = srv.get("command")
        if cmd:
            out.append(cmd)
    return out, None


if __name__ == "__main__":
    sys.exit(main())
