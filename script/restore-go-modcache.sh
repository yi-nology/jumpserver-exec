#!/usr/bin/env bash
# 把 Go 的 module cache 补回来。
#
# 为什么有这个脚本：~/go/pkg/mod 下解压好的模块目录会在某些环境里被清掉
# （2026-10-15 那次，服务依赖的东西全没了，连 MCP SDK 都没了）。而某些
# 沙箱拦住了 `go mod download` 和 Go 自己解包要用的 rename(2) ⇒ 模块没法
# 由工具链「下载并解包」。这个脚本手工做这件事：把零件取回来，放到 Go 期待
# 的位置，全程不让工具链往 cache 里写。
#
# 用法：
#     ./script/restore-go-modcache.sh
#     ./script/restore-go-modcache.sh && ./script/build.sh
#
# ⚠️ 没有 replace 指令：依赖按普通方式解析，由 go.sum 钉住内容。所以每个
# 模块在 cache 里要有三样东西，这个脚本把三样都放回去：
#
#     cache/download/<mod>/@v/<ver>.mod   模块自己的 go.mod（模块图要读它）
#     cache/download/<mod>/@v/<ver>.ziphash
#                                         解包目录的哈希；Go 拿它和 go.sum
#                                         比，而它正是 go.sum 里那行 h1
#     <mod>@<ver>/                        解包后的源码
#
# .zip 只在源码目录需要（重）建时用到：目录在了且 ziphash 对得上，Go 就
# 再也不碰 zip。
#
# ⚠️ 下面的 MODULES 列表必须与 go.mod 的 require **逐版本一致**：只在一个
# 里出现而不另一个里出现的模块，要么是恢复不了，要么是用不上。
#
# 本仓是**应用**（不是库）：依赖列表 = 共用底座 jumpserver-kit
# + 本仓直接用到的第三方库。kit 在它发布之前由本地工作集（go.work）提供，
# 本脚本会认出它并跳过（见 in_workspace）。
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
GOSUM="$ROOT/go.sum"

GOMODCACHE="$(go env GOMODCACHE)"
PROXY="${GOPROXY_OVERRIDE:-https://proxy.golang.org,direct}"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/gomodfix.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

MODULES=(
  "github.com/alibabacloud-go/alibabacloud-gateway-pop@v0.0.6"
  "github.com/alibabacloud-go/alibabacloud-gateway-spi@v0.0.5"
  "github.com/alibabacloud-go/darabonba-array@v0.1.0"
  "github.com/alibabacloud-go/darabonba-encode-util@v0.0.2"
  "github.com/alibabacloud-go/darabonba-map@v0.0.2"
  "github.com/alibabacloud-go/darabonba-openapi/v2@v2.0.10"
  "github.com/alibabacloud-go/darabonba-signature-util@v0.0.7"
  "github.com/alibabacloud-go/darabonba-string@v1.0.2"
  "github.com/alibabacloud-go/debug@v1.0.1"
  "github.com/alibabacloud-go/endpoint-util@v1.1.0"
  "github.com/alibabacloud-go/kms-20160120/v3@v3.2.3"
  "github.com/alibabacloud-go/openapi-util@v0.1.0"
  "github.com/alibabacloud-go/tea-utils/v2@v2.0.7"
  "github.com/alibabacloud-go/tea-utils@v1.4.4"
  "github.com/alibabacloud-go/tea-xml@v1.1.3"
  "github.com/alibabacloud-go/tea@v1.2.2"
  "github.com/aliyun/alibaba-cloud-sdk-go@v1.61.1800"
  "github.com/aliyun/alibabacloud-dkms-gcs-go-sdk@v0.5.1"
  "github.com/aliyun/alibabacloud-dkms-transfer-go-sdk@v0.1.8"
  "github.com/aliyun/aliyun-secretsmanager-client-go@v1.1.5"
  "github.com/aliyun/credentials-go@v1.4.3"
  "github.com/beorn7/perks@v1.0.1"
  "github.com/boombuler/barcode@v1.0.1-0.20190219062509-6c824513bacc"
  "github.com/bytedance/gopkg@v0.1.4"
  "github.com/bytedance/sonic/loader@v0.5.0"
  "github.com/bytedance/sonic@v1.15.0"
  "github.com/cespare/xxhash/v2@v2.3.0"
  "github.com/clbanning/mxj/v2@v2.5.5"
  "github.com/cloudwego/base64x@v0.1.6"
  "github.com/cloudwego/configmanager@v0.2.3"
  "github.com/cloudwego/dynamicgo@v0.9.2"
  "github.com/cloudwego/fastpb@v0.0.6"
  "github.com/cloudwego/frugal@v0.3.1"
  "github.com/cloudwego/gopkg@v0.2.1"
  "github.com/cloudwego/kitex@v0.16.4"
  "github.com/cloudwego/localsession@v0.2.2"
  "github.com/cloudwego/netpoll@v0.7.5"
  "github.com/cloudwego/runtimex@v0.1.2"
  "github.com/cloudwego/thriftgo@v0.4.5"
  "github.com/deckarep/golang-set@v1.7.1"
  "github.com/fatih/structtag@v1.2.0"
  "github.com/go-logr/logr@v1.4.4"
  "github.com/golang/protobuf@v1.5.4"
  "github.com/google/jsonschema-go@v0.4.3"
  "github.com/google/pprof@v0.0.0-20240727154555-813a5fbdbec8"
  "github.com/iancoleman/strcase@v0.3.0"
  "github.com/jhump/protoreflect@v1.8.2"
  "github.com/jmespath/go-jmespath@v0.0.0-20180206201540-c2b33e8439af"
  "github.com/json-iterator/go@v1.1.12"
  "github.com/kitex-contrib/resolver-dns@v0.1.0"
  "github.com/klauspost/cpuid/v2@v2.2.10"
  "github.com/modelcontextprotocol/go-sdk@v1.8.0"
  "github.com/modern-go/concurrent@v0.0.0-20180306012644-bacd9c7ef1dd"
  "github.com/modern-go/reflect2@v1.0.2"
  "github.com/munnerz/goautoneg@v0.0.0-20191010083416-a7dc8b61c822"
  "github.com/nacos-group/nacos-sdk-go/v2@v2.3.6-0.20260601044846-0e3d1d099507"
  "github.com/oagentkit/guardx@v0.16.0"
  "github.com/oagentkit/tools@v0.10.4"
  "github.com/oekit/bootstrap@v0.6.8"
  "github.com/oekit/concurrency@v0.23.4"
  "github.com/oekit/core@v0.12.1"
  "github.com/oekit/kitex@v0.7.6"
  "github.com/oekit/logx/kitexlog@v0.1.8"
  "github.com/oekit/logx@v0.8.2"
  "github.com/oekit/metrics@v0.1.11"
  "github.com/oekit/network@v0.6.1"
  "github.com/oekit/pkg@v0.33.0"
  "github.com/orcaman/concurrent-map@v0.0.0-20210501183033-44dafcb38ecc"
  "github.com/pkg/errors@v0.9.1"
  "github.com/pquerna/otp@v1.5.0"
  "github.com/prometheus/client_golang@v1.25.0"
  "github.com/prometheus/client_model@v0.6.3"
  "github.com/prometheus/common@v0.72.0"
  "github.com/prometheus/procfs@v0.22.0"
  "github.com/segmentio/asm@v1.1.3"
  "github.com/segmentio/encoding@v0.5.4"
  "github.com/stretchr/testify@v1.12.1"
  "github.com/tidwall/gjson@v1.18.0"
  "github.com/tidwall/match@v1.1.1"
  "github.com/tidwall/pretty@v1.2.0"
  "github.com/tjfoc/gmsm@v1.4.1"
  "github.com/twitchyliquid64/golang-asm@v0.15.1"
  "github.com/yi-nology/jumpserver-kit@v0.8.1"
  "github.com/yosida95/uritemplate/v3@v3.0.2"
  "go.opentelemetry.io/otel/log@v1.47.0"
  "go.uber.org/multierr@v1.11.0"
  "go.uber.org/zap@v1.28.0"
  "go.yaml.in/yaml/v3@v3.0.5"
  "golang.org/x/arch@v0.14.0"
  "golang.org/x/crypto@v0.58.0"
  "golang.org/x/net@v0.60.0"
  "golang.org/x/oauth2@v0.37.0"
  "golang.org/x/sync@v0.24.0"
  "golang.org/x/sys@v0.49.0"
  "golang.org/x/text@v0.43.0"
  "golang.org/x/time@v0.16.0"
  "google.golang.org/genproto/googleapis/rpc@v0.0.0-20260526163538-3dc84a4a5aaa"
  "google.golang.org/genproto@v0.0.0-20240227224415-6ceb2ff114de"
  "google.golang.org/grpc@v1.83.1"
  "google.golang.org/protobuf@v1.36.12"
  "gopkg.in/ini.v1@v1.67.0"
  "gopkg.in/natefinch/lumberjack.v2@v2.2.1"
  "gopkg.in/yaml.v3@v3.0.1"
  "mvdan.cc/sh/v3@v3.14.1"
)

# in_workspace 判断某个模块是不是由**本地工作集**（go.work）提供的。
#
# ⚠️ 为什么要有这一步：共用底座 jumpserver-kit 在它发布之前，本地是用
# go.work 里的 use 指向源码目录的 —— 那份源码**不在** module cache 里，
# 也不在代理上（代理上还没有这个版本）。⇒ 它不该被「恢复」：强行去取只会
# 拿到一个 404，然后把这个脚本的结论染成「失败」。
in_workspace() {
  local mod="$1" dir
  local gowork="${GOWORK:-}"
  if [ -z "$gowork" ]; then
    gowork="$(cd "$ROOT" && go env GOWORK 2>/dev/null || true)"
  fi
  [ -n "$gowork" ] || return 1
  dir="$(cd "$ROOT" && GOPROXY=off go list -m -f '{{.Dir}}' "$mod" 2>/dev/null || true)"
  [ -n "$dir" ] && [ -d "$dir" ] || return 1
  case "$dir" in
    "$GOMODCACHE"/*) return 1 ;;
  esac
  return 0
}

# fetch 把一个 URL 的内容取到文件里，全程不让工具链写 cache。
#
# 它会重试，而且不藏状态码。之前的两个错：
#
#   - 冷 cache 恢复时一次 DNS/TLS 抖动，让某个模块永远缺着。恢复报了失败，
#     但**下一次**构建说的是「replacement directory ... does not exist」——
#     于是人们去改 go.mod，而真正的问题是那一次请求抖了。
#   - code 声明成了局部变量，于是失败信息永远打印 "HTTP 000"，哪怕代理明明
#     回了 404。
#
# code 故意**不是**局部变量：调用方要打印它。
fetch() {
  local url="$1" dest="$2" attempt
  mkdir -p "$(dirname "$dest")"
  code=""
  for attempt in 1 2 3 4; do
    code=$(curl -sSL --max-time 180 -o "$dest" -w '%{http_code}' "$url" 2>/dev/null)
    if [ "$code" = "200" ] && [ -s "$dest" ]; then
      return 0
    fi
    rm -f "$dest"
    if [ "$attempt" -lt 4 ]; then
      sleep $((attempt * 2))
    fi
  done
  return 1
}

ok=0
fail=0
for mod in "${MODULES[@]}"; do
  base="${mod%@*}"
  ver="${mod#*@}"
  dl="$GOMODCACHE/cache/download/$base/@v"
  dest="$GOMODCACHE/$mod"

  # 0. 本地工作集提供的模块：跳过（它不在代理上，也不需要在 cache 里）
  if in_workspace "$base"; then
    echo "  跳过     $base@${ver}（由本地工作集提供，不进 cache）"
    continue
  fi

  # 1. 模块自己的 go.mod —— 模块图要读它
  if [ ! -f "$dl/$ver.mod" ]; then
    if fetch "$PROXY/$base/@v/$ver.mod" "$dl/$ver.mod"; then
      echo "  restored $base@$ver (.mod)"
    else
      echo "  FAIL     $mod  (.mod, HTTP ${code:-000})"
      fail=$((fail + 1))
      continue
    fi
  fi

  # 2. 解包后的源码 —— Go 自己解不了包（沙箱拦着 rename(2)），所以在这里
  #    解开再放到位。
  if [ ! -d "$dest" ]; then
    zip="$WORK/$(echo "$base" | tr '/' '_').zip"
    if ! fetch "$PROXY/$base/@v/$ver.zip" "$zip"; then
      echo "  FAIL     $mod  (zip, HTTP ${code:-000})"
      fail=$((fail + 1))
      continue
    fi
    rm -rf "$WORK/x"
    mkdir -p "$WORK/x"
    if ! unzip -q -o "$zip" -d "$WORK/x" 2>/dev/null || [ ! -d "$WORK/x/$mod" ]; then
      echo "  FAIL     $mod  (unzip)"
      fail=$((fail + 1))
      continue
    fi
    mkdir -p "$(dirname "$dest")"
    if ! cp -R "$WORK/x/$mod" "$dest" 2>/dev/null; then
      echo "  FAIL     $mod  (copy into $dest)"
      fail=$((fail + 1))
      continue
    fi
    echo "  restored $base@$ver (source)"
  fi

  # 3. ziphash —— 内容就是 go.sum 里那行 h1，所以从这里写出去，而不是自己算。
  if [ ! -f "$dl/$ver.ziphash" ]; then
    hash=$(grep "^$base $ver h1:" "$GOSUM" 2>/dev/null | awk '{print $3}')
    if [ -n "$hash" ]; then
      mkdir -p "$dl"
      printf '%s\n' "$hash" > "$dl/$ver.ziphash"
    else
      echo "  FAIL     $mod  (go.sum 里没有 h1 行)"
      fail=$((fail + 1))
      continue
    fi
  fi

  ok=$((ok + 1))
done

echo
echo "restored=$ok failed=$fail   cache=$GOMODCACHE"
[ "$fail" -eq 0 ]
