#!/usr/bin/env bash
# 编译本仓的二进制。
#
# 为什么有这个脚本：一个仓一个二进制，「怎么编」就该有一句能照着抄的命令。
# 拆仓之前三个仓共用一个 build-mcp.sh（一次编两个二进制、跑一次全仓测试、
# 再把产物装进 ~/.local/bin）—— 那是给一台机器上的运维用的，clone 下来的
# OSS 用户看不懂它到底编了什么，所以这里每个仓只留一个只做一件事的脚本。
#
# 用法：
#     ./script/build.sh                 # 编译 + 跑测试（默认）
#     ./script/build.sh --no-test       # 只编译
#     ./script/build.sh --help
#
# 产物：
#     ./bin/jumpserver-mcp-exec   ← 本仓唯一的二进制
#
# 环境：
#     GOPROXY / GOFLAGS  一律按 Go 的默认走，不在本脚本里写死。
#     离线机器上可以 GOPROXY=off ./script/build.sh —— 只要 module cache
#     里已经有依赖就能编（缺依赖时见 script/restore-go-modcache.sh）。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

BIN_NAME="jumpserver-mcp-exec"
BIN_DIR="$ROOT/bin"

RUN_TESTS=1
for arg in "$@"; do
  case "$arg" in
    --no-test) RUN_TESTS=0 ;;
    --help|-h)
      sed -n '2,22p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      echo "unknown option: $arg (try --help)" >&2
      exit 2
      ;;
  esac
done

cd "$ROOT"

echo "==> 编译 $BIN_NAME"
mkdir -p "$BIN_DIR"
go build -o "$BIN_DIR/$BIN_NAME" "."
echo "    产物：$BIN_DIR/$BIN_NAME"

if [ "$RUN_TESTS" -eq 1 ]; then
  echo "==> 跑测试"
  go test ./...
  echo "    全部通过"
fi

echo "==> 完成"
echo "    跑起来：./bin/$BIN_NAME"
