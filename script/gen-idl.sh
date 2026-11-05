#!/usr/bin/env bash
# 从 IDL 重新生成契约模型。
#
# 为什么有这个脚本：契约（IDL）是承诺，模型（生成的 Go 类型）是兑现。
# 生成的代码**入库**（Go 仓里所有生成物都入库）：构建不该依赖 thriftgo 装没装，
# 而模型是审阅时要读的那一份。
#
# 为什么用 slim 模板：默认模板会 import github.com/apache/thrift/lib/go/thrift，
# 顺带拖进一整套 Thrift 编解码运行时 —— 而这个服务说的是 JSON-RPC，不是
# Thrift。slim 只出结构体和 JSON tag。
#
# 用法：
#     ./script/gen-idl.sh              # 处理本仓的 idl/（摊平布局）
#     ./script/gen-idl.sh <目录>...    # 处理指定的 IDL 目录
#
# 需要 thriftgo：
#     go install github.com/cloudwego/thriftgo@latest
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# 本仓的 IDL 与它的落点：idl/（单词汇仓摊平布局）的入口 thrift → model/<包名>/
# 与 idl/<包名>/（多词汇仓）都认：入口名取自文件一致声明的 `namespace go`，
# 不取目录名 —— 平铺的 idl/ 目录名是 "idl"，不命名任何包。
#
# -r 不是可选项：一份 IDL 按域拆成同目录多份 thrift（入口 <包名>.thrift
# include 各域文件），没有 -r 时 thriftgo 只生成入口那份 —— 各域声明的类型
# 一个生成物都没有，包根本编不过。
generate() {
  local idl="$1" out="$2"
  [[ -f "$idl" ]] || return 0
  if ! command -v thriftgo >/dev/null 2>&1; then
    echo "⚠️  找不到 thriftgo（go install github.com/cloudwego/thriftgo@latest）" >&2
    return 1
  fi
  echo "==> 生成 ${idl#$ROOT/}"
  mkdir -p "$out"
  thriftgo --gen go:template=slim -r -o "$out" "$idl"
}

# entryOf 找一个 IDL 目录的入口 thrift：该目录第一份 `namespace go` 声明的
# 包名 + .thrift。嵌套 idl/<pkg>/ 下它就是 <pkg>.thrift（旧行为）；平铺
# idl/ 下目录名不命名包，只能问文件。
entryOf() {
  local dir="$1"
  local ns
  ns="$(grep -h '^namespace go ' "$dir"/*.thrift | head -1 | awk '{print $3}')"
  [[ -n "$ns" ]] || { echo "⚠️  $dir 里没有 namespace go 声明" >&2; return 1; }
  echo "$dir/$ns.thrift"
}

if [[ $# -gt 0 ]]; then
  for dir in "$@"; do
    [[ "$dir" = /* ]] || dir="$ROOT/$dir"
    generate "$(entryOf "$dir")" "$ROOT/biz/model"
  done
else
  generate "$(entryOf "$ROOT/idl")" "$ROOT/biz/model"
fi

echo "==> 完成；记得把重新生成的模型一起提交"
