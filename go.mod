// jumpserver-mcp-exec —— JumpServer 堡垒机的**执行面** MCP 服务。
//
// 经堡垒机 koko(2222) 在受管机器上跑命令、传文件。15 个工具。
//
// 共用的底座在 jumpserver-mcp-kit（协议层、门禁、契约、留痕、脱敏、风险分类），
// 本仓只放**这个服务自己的**东西：koko TUI 协议实现、工具实现、输出契约。
//
// ⚠️ 不写 replace：依赖按普通方式从 module cache 解析，go.sum 钉住内容。

module github.com/yi-nology/jumpserver-mcp-exec

go 1.26.0

require (
	github.com/yi-nology/jumpserver-mcp-kit v0.1.9

	github.com/google/jsonschema-go v0.4.3
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/oagentkit/guardx v0.16.0
	github.com/oagentkit/tools v0.10.4
	github.com/oekit/concurrency v0.23.0
	github.com/oekit/core v0.12.1
	github.com/oekit/logx v0.8.0
	github.com/oekit/network v0.6.1
	github.com/oekit/pkg v0.31.0
	github.com/pquerna/otp v1.5.0
	golang.org/x/crypto v0.58.0
	golang.org/x/time v0.16.0
	gopkg.in/yaml.v3 v3.0.1
)

require (

	github.com/boombuler/barcode v1.0.1-0.20190219062509-6c824513bacc // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	mvdan.cc/sh/v3 v3.14.1 // indirect
)
