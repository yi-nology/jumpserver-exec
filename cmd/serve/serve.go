// Package serve is the "serve" subcommand: MCP over stdio.
//
// ⚠️ 它是**子命令的实现**，不是二进制：入口层（仓根那个 package main）
// import 它。一个仓一个二进制 ⇒ cmd/ 下的每个包都只提供「一段活」，
// 谁都不许再长出一个自己的 main。
//
// ⚠️ 留痕接在这里，而不是接在入口层：留痕是「**谁在这台机器上做了什么**」
// 的记录，只有**跑服务**那条路会调工具 ⇒ 「列工具目录」那种命令不该
// 留下记录。
package serve

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/oekit/pkg/cli"

	"github.com/yi-nology/jumpserver-mcp-exec/biz/router"
	"github.com/yi-nology/jumpserver-mcp-exec/internal/config"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/audit"
)

// Run serves MCP over stdio until the transport closes.
//
// ⚠️ 版本从入口层递进来：版本是**二进制的属性**，而版本常量住在仓根
// （package main）⇒ 子命令拿不到它。
func Run(ctx context.Context, version string, args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "jumpserver-mcp-exec: %v\n", err)
		return cli.ExitError
	}

	// The inventory tool needs the REST client; the command tools do not.
	// Fail soft so the server still starts and the exec tools keep working.
	c := cfg.APIClient()
	if c == nil {
		fmt.Fprintf(os.Stderr, "jumpserver-mcp-exec: REST client unavailable: JUMPSERVER_TOKEN is not set (jmsx_inventory disabled)\n")
	}

	r := router.New(cfg, c, version, router.Options{
		// ⚠️ The trail is wired HERE, on the path that actually calls
		// tools: a DANGEROUS command refused for want of a confirmation
		// never reaches the bastion, so the bastion has no session to
		// show for it. Without this line, "who tried to run rm -rf on
		// that box and was stopped" has no answer anywhere.
		//
		// ⚠️ It matters MORE on this plane than on the management one:
		// this is the plane where the commands actually run.
		OnRecord: audit.Trail(cfg.Audit.Options(router.ServerName)),
	})
	if err := r.Serve(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "jumpserver-mcp-exec: server failed: %v\n", err)
		return cli.ExitError
	}
	return cli.ExitOK
}
