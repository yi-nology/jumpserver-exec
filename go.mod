// jumpserver-exec —— JumpServer 堡垒机的**执行面** MCP 服务。
//
// 经堡垒机 koko(2222) 在受管机器上跑命令、传文件。15 个工具。
//
// 共用的底座在 jumpserver-kit（协议层、门禁、契约、留痕、脱敏、风险分类），
// 本仓只放**这个服务自己的**东西：koko TUI 协议实现、工具实现、输出契约。
//
// ⚠️ 不写 replace：依赖按普通方式从 module cache 解析，go.sum 钉住内容。

module github.com/yi-nology/jumpserver-exec

go 1.27.0

require (
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/oekit/concurrency v0.33.0
	github.com/oekit/logx v0.9.2
	github.com/oekit/network v0.10.1
	github.com/oekit/pkg v0.44.0
	github.com/yi-nology/jumpserver-kit v0.13.0
	golang.org/x/crypto v0.59.0
)

require (
	github.com/alibabacloud-go/alibabacloud-gateway-pop v0.1.6 // indirect
	github.com/alibabacloud-go/alibabacloud-gateway-spi v0.0.5 // indirect
	github.com/alibabacloud-go/darabonba-array v0.1.0 // indirect
	github.com/alibabacloud-go/darabonba-encode-util v0.0.2 // indirect
	github.com/alibabacloud-go/darabonba-map v0.0.2 // indirect
	github.com/alibabacloud-go/darabonba-openapi/v2 v2.2.5 // indirect
	github.com/alibabacloud-go/darabonba-string v1.0.2 // indirect
	github.com/alibabacloud-go/debug v1.0.1 // indirect
	github.com/alibabacloud-go/kms-20160120/v3 v3.4.0 // indirect
	github.com/alibabacloud-go/openapi-util v0.1.2 // indirect
	github.com/alibabacloud-go/tea v1.5.4 // indirect
	github.com/alibabacloud-go/tea-utils/v2 v2.0.9 // indirect
	github.com/aliyun/alibaba-cloud-sdk-go v1.63.107 // indirect
	github.com/aliyun/alibabacloud-dkms-gcs-go-sdk v0.5.1 // indirect
	github.com/aliyun/alibabacloud-dkms-transfer-go-sdk v0.1.9 // indirect
	github.com/aliyun/aliyun-secretsmanager-client-go v1.1.5 // indirect
	github.com/aliyun/credentials-go v1.4.14 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/boombuler/barcode v1.1.0 // indirect
	github.com/bytedance/gopkg v0.1.4 // indirect
	github.com/bytedance/sonic v1.15.5 // indirect
	github.com/bytedance/sonic/loader v0.5.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/clbanning/mxj/v2 v2.7.0 // indirect
	github.com/cloudwego/base64x v0.1.7 // indirect
	github.com/cloudwego/configmanager v0.2.3 // indirect
	github.com/cloudwego/dynamicgo v0.9.2 // indirect
	github.com/cloudwego/fastpb v0.0.6 // indirect
	github.com/cloudwego/frugal v0.3.1 // indirect
	github.com/cloudwego/gopkg v0.2.1 // indirect
	github.com/cloudwego/kitex v0.16.4 // indirect
	github.com/cloudwego/localsession v0.2.2 // indirect
	github.com/cloudwego/netpoll v0.7.5 // indirect
	github.com/cloudwego/runtimex v0.1.2 // indirect
	github.com/cloudwego/thriftgo v0.4.5 // indirect
	github.com/deckarep/golang-set v1.8.0 // indirect
	github.com/fatih/structtag v1.2.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/pprof v0.0.0-20261111065629-49901cbc7227 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/iancoleman/strcase v0.3.0 // indirect
	github.com/jhump/protoreflect v1.19.0 // indirect
	github.com/jhump/protoreflect/v2 v2.0.0 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kitex-contrib/resolver-dns v0.1.0 // indirect
	github.com/klauspost/cpuid/v2 v2.4.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nacos-group/nacos-sdk-go/v2 v2.3.6 // indirect
	github.com/oagentkit/guardx v0.23.4 // indirect
	github.com/oagentkit/tools v0.13.4 // indirect
	github.com/oekit/bootstrap v0.7.6 // indirect
	github.com/oekit/core v0.14.2 // indirect
	github.com/oekit/kitex v0.7.18 // indirect
	github.com/oekit/logx/kitexlog v0.1.11 // indirect
	github.com/oekit/metrics v0.3.0 // indirect
	github.com/opentracing/opentracing-go v1.2.1-0.20220228012449-10b1cf09e00b // indirect
	github.com/orcaman/concurrent-map v1.0.0 // indirect
	github.com/pandatix/go-abnf v0.5.5 // indirect
	github.com/petermattis/goid v0.0.0-20261109124353-7cfa343ad0ab // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pquerna/otp v1.5.0 // indirect
	github.com/prometheus/client_golang v1.25.0 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.73.1 // indirect
	github.com/prometheus/procfs v0.23.0 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/tidwall/gjson v1.20.2 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.3.0 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.28.0 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/arch v0.33.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/oauth2 v0.38.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261110000943-09e21b237f2c // indirect
	google.golang.org/grpc v1.85.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/ini.v1 v1.67.3 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	mvdan.cc/sh/v3 v3.14.1 // indirect
)
