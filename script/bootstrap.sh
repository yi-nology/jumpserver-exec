#!/usr/bin/env bash
# 把本仓的服务跑起来（交互式 stdio）。
#
# 为什么有这个脚本：MCP 服务是**stdio 上说 JSON-RPC** 的，agent 运行时启动它
# 时不带任何参数。所以「手工跑一下看看它起不起来」这件事本身是有用的 ——
# 它能立刻回答「这个进程能不能起来」，而不是让人去猜。
#
# 用法：
#     ./script/build.sh && ./script/bootstrap.sh
#     JMS_BIN_DIR=/some/where ./script/bootstrap.sh    # 跑别处安装的那份
#
# 它启动的是**已安装**的那一份：script/build.sh 把它编到 ./bin/，
# script/publish-mcp.py 把它发布到 ~/.local/<...>-runtime/<时间戳>/。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

NAME="jumpserver-mcp-exec"

# 本仓刚编出来的那一份优先 —— 它就是「我现在这份源码」。
if [ -x "$ROOT/bin/$NAME" ]; then
  exec "$ROOT/bin/$NAME"
fi

# 其次是运维指定的目录（发布脚本把副本放在那儿）。
BIN_DIR="${JMS_BIN_DIR:-$HOME/.local/bin}"
if [ -x "$BIN_DIR/$NAME" ]; then
  exec "$BIN_DIR/$NAME"
fi

echo "$NAME 既不在 $ROOT/bin/ 也不在 $BIN_DIR/；先跑 ./script/build.sh" >&2
exit 1
