// Package tools is the "tools" subcommand: the catalogue.
//
// ⚠️ 它是**审计视角**下的工具目录：每个工具改不改机器、要不要确认、
// 确认时说什么话，都在这儿列出来。
package tools

import (
	"context"
	"fmt"
	"io"

	"github.com/oekit/pkg/cli"

	"github.com/yi-nology/jumpserver-mcp-exec/biz/router"
	"github.com/yi-nology/jumpserver-mcp-exec/internal/config"
	"github.com/yi-nology/jumpserver-mcp-exec/internal/service/policy"
)

// Run lists the tool catalogue: name, risk, and what it asks for.
//
// ⚠️ 版本从入口层递进来：版本是**二进制的属性**，而版本常量住在仓根
// （package main）⇒ 子命令拿不到它。
func Run(ctx context.Context, version string, args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		// The catalogue does not need a bastion, and it does not need a
		// configured one either: this is the audit view of the policy table.
		//
		// ⚠️ So a broken configuration is reported but does NOT stop the
		// listing — the management plane does the same, and a server that
		// answers "I cannot list my own tools because you forgot a
		// variable" is a worse answer than the catalogue.
		fmt.Fprintf(stderr, "jumpserver-mcp-exec: %v\n", err)
		cfg = config.Config{}
	}
	r := router.New(cfg, cfg.APIClient(), version, router.Options{})
	tbl := cli.NewTable()
	for _, spec := range r.Specs() {
		confirm := ""
		if spec.Confirm != nil {
			confirm = *spec.Confirm
		} else if p, ok := policy.Table[spec.Name]; ok {
			confirm = p.Hint
		}
		tbl.Append(spec.Name, spec.Risk.String(), confirm)
	}
	tbl.Render(stdout)
	return cli.ExitOK
}
