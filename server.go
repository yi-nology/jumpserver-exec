package main

// 这一面**跑起来**是什么样。
//
// ⚠️ 服务本身（配置、装配、留痕、stdio 上的 MCP）在 cmd/serve/ ——
// 子命令的实现在那儿；入口层只负责「没有子命令时跑哪个」。
//
// ⚠️ 「没有子命令」在这类程序里意味着「**跑起来**」，不是「用法错误」：
// MCP 框架启动这个二进制时**不带任何参数** ⇒ 默认命令必须是 serve。

import (
	"context"
	"io"

	"github.com/yi-nology/jumpserver-mcp-exec/cmd/serve"
)

// defaultCommand is the command that runs when there is no subcommand.
//
// ⚠️ 它必须是 `serve`：框架启动这个二进制时不带参数，而
// 「没有子命令」在这类程序里不是用法错误。
const defaultCommand = "serve"

// defaultRun runs the default command — which is the "serve" subcommand.
//
// ⚠️ 版本从入口层递进去：版本是**二进制的属性**，而版本常量住在仓根
// （package main）⇒ 子命令拿不到它。
func defaultRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return serve.Run(ctx, Version, args, stdout, stderr)
}
