#!/usr/bin/env bash
# 从工具目录重新生成 swagger/openapi.json。
#
# 为什么有这个脚本：HTTP 面的 OpenAPI 不是手写的 —— 手写的一份必然与它
# 服务的工具漂移。这里的文档由 `tools openapi` 生成（与 MCP 工具列表同一份
# 注册表、同一份 schema），生成物**入库**，仓里的守卫（openapi_test.go）
# 拿它和再生成的字节比对：改了工具不重新生成，测试就红。
#
# 用法（在仓库根）：
#     ./script/gen-openapi.sh
#
# 生成是确定性的：同一个工具目录渲染出同样的字节（map 键排序）。
# 生成**不需要**堡垒机与任何秘密：`tools openapi` 用契约桩 client 渲染
# 完整目录（15 个工具），文档不随生成机器的凭据变。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "==> 生成 swagger/openapi.json（tools openapi）"
cd "$ROOT"
go run . tools openapi > "$ROOT/swagger/openapi.json"

echo "==> 完成；记得把重新生成的 openapi.json 一起提交"
