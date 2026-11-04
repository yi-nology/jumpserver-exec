package main

// 命令行的**装配**：应用叫什么、什么版本、有哪些子命令、没有子命令时跑
// 哪个。
//
// ⚠️ 为什么装配住在 init.go 而不是 main.go：main.go 说的是「这个程序是
// 什么」（包文档、版本、进程入口），init.go 说的是「它启动起来之后是
// 什么样子」（那张命令表）。两件事分开读，比挤在一个文件里好读。
//
// ⚠️ 退出码是 oekit's：0 ok，1 error，2 usage。
// ⚠️ 日志后端写 **stderr**，绝不写 stdout：stdout 走 JSON-RPC，一个日志
// 行落进去就把协议弄坏了。

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/oekit/logx"
	"github.com/oekit/pkg/cli"

	"github.com/yi-nology/jumpserver-mcp-exec/cmd/doctor"
	"github.com/yi-nology/jumpserver-mcp-exec/cmd/tools"
)

// dispatch runs the command line and exits with the command's exit code.
func dispatch() {
	// The log backend writes to stderr, never to stdout: stdout carries the
	// JSON-RPC stream, and a log line there would corrupt the protocol.
	logx.SetDefault(logx.NewSlogLogger("jumpserver-mcp-exec",
		logx.WithHandler(slog.NewJSONHandler(os.Stderr, nil))))

	app := &cli.App{
		Name:    "jumpserver-mcp-exec",
		Version: Version,
		// No arguments means "run the default way": this server is started by
		// a framework that passes no subcommand name, and "no subcommand"
		// in a program like this one means "serve", not "usage error".
		Default: defaultCommand,
		Commands: []cli.Command{
			{Name: "serve", Summary: "以 stdio 提供 MCP 服务（默认）", Run: defaultRun},
			// The flag spelling is kept for compatibility: it used to be
			// this server's only spelling before it became a subcommand.
			{Name: "doctor", Summary: "检查 koko 接线（不需要 MCP 会话）",
				Aliases: []string{"--doctor", "-doctor"}, Run: doctor.Run},
			{Name: "tools", Summary: "列出工具目录：名字、风险", Run: toolsRun},
		},
	}
	code := app.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	// ⚠️ 收尾在退出码**算出来之后**跑 ⇒ 它不许改退出码（退出码是命令层
	// 的判据，见 shutdown.go 的契约）。
	shutdown()
	os.Exit(code)
}

// toolsRun is the "tools" subcommand's entry point.
//
// ⚠️ 版本是**二进制的属性**，而版本常量住在仓根（package main）⇒ 子命令
// 拿不到它 ⇒ 它从入口层递进来。
func toolsRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return tools.Run(ctx, Version, args, stdout, stderr)
}
