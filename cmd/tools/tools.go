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

	"github.com/yi-nology/jumpserver-exec/biz/router"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-exec/internal/service/policy"
	"github.com/yi-nology/jumpserver-kit/pkg/httpapi"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
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
		fmt.Fprintf(stderr, "jumpserver-exec: %v\n", err)
		cfg = config.Config{}
	}
	r := router.New(cfg, cfg.APIClient(), version, router.Options{})

	// `tools openapi` renders the HTTP surface's OpenAPI document. The
	// checked-in copy under swagger/ is generated through this exact code
	// path (script/gen-openapi.sh) — so the document and the tools cannot
	// drift apart, and the service's own guard compares them.
	//
	// ⚠️ It renders from router.NewContract, not from the registry above:
	// `jmsx_inventory` only registers when a REST client is configured, and
	// a checked-in contract must not be a function of the machine that
	// generated it.
	if len(args) > 0 && args[0] == "openapi" {
		doc, err := httpapi.OpenAPI(router.NewContract(version), httpapi.Options{Name: router.ServerName, Version: version, ReadOnly: cfg.ReadOnly})
		if err != nil {
			fmt.Fprintf(stderr, "FATAL  %v\n", err)
			return cli.ExitError
		}
		if _, err := stdout.Write(doc); err != nil {
			fmt.Fprintf(stderr, "FATAL  %v\n", err)
			return cli.ExitError
		}
		return cli.ExitOK
	}

	// The audit view of the catalogue. A CONDITIONAL confirmation has no
	// sentence to refuse with, but the rule is still worth showing: the
	// hint lookup is this service's own policy table (the risk side lives
	// here, so the shared table renderer takes it as a callback).
	reg.CatalogueTable(stdout, r.Specs(), func(name string) string {
		if p, ok := policy.Table[name]; ok {
			return p.Hint
		}
		return ""
	})
	return cli.ExitOK
}
