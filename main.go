// Command jumpserver-mcp-exec runs commands and moves files on machines managed by
// JumpServer, through the bastion's koko TUI — so every session is recorded
// and auditable.
//
// Transport: stdio, the same as the management plane. Credentials: an RSA
// SSH key registered as a JumpServer user (koko rejects password auth for
// automation) plus a PrivateToken for the read-only inventory tool.
//
// This is the command line of the execution plane. The normal mode is
// serving MCP over stdio: the agent runtime starts the binary with no
// arguments and speaks JSON-RPC over stdin/stdout. The subcommands exist for
// the moments when there is no session yet — the wiring is suspect and the
// tools are not answering.
//
// The exit codes are oekit's: 0 ok, 1 error, 2 usage.
//
// ⚠️ **入口层在仓根**：这一面是一个仓一个二进制，二进制就是仓根那个
// package main（main.go / init.go / server.go / workers.go / shutdown.go）。
// 子命令的**实现**在 cmd/ 下（cmd/serve、cmd/doctor、cmd/tools）——
// 那是**入口层 import 的普通包**，不是各自的二进制。
//
// ⚠️ 子命令的实现为什么不住在仓根：仓根那个 package 是 `main`，
// 而 Go **不许 import 一个 main 包** ⇒ 住在仓根的实现，子命令用不了；
// 反过来也一样 —— 子命令的实现住在 cmd/ 下，入口层 import 它们。
//
// 这一面的布局（CloudWeGo hz 的标准骨架）：
//
//	main.go / init.go / server.go / workers.go / shutdown.go  ← 入口层（仓根）
//	cmd/{serve,doctor,tools}/     ← 子命令（入口层 import 的包）
//	biz/handler/                   ← 工具
//	biz/model/jmsexec/             ← 契约，IDL 生成的
//	biz/router/                    ← 工具目录与装配
//	internal/config/               ← 配置（环境变量）
//	internal/repository/koko/      ← 数据访问层（koko TUI 协议与文件传输）
//	internal/service/policy/        ← 工具目录的风险侧
//	idl/jmsexec/                   ← 契约，thrift
//
// 共用底座（协议层、门禁、结果整形、留痕、脱敏、失败分类）在**另一个仓**：
// github.com/yi-nology/jumpserver-mcp-kit。

package main

// Version is the version of the execution-plane server.
//
// ⚠️ **它必须与本仓的 tag 一致** —— `go install ...@v0.4.2` 装出来的二进制
// 要自己说对版本；说错的话，用户看 `--version` 会以为装错了版本。
const Version = "0.4.2"

func main() {
	dispatch()
}
