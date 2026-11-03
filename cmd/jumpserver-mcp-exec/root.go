package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oekit/logx"
	"github.com/oekit/pkg/cli"

	"github.com/yi-nology/jumpserver-mcp-exec/biz/exec/policy"
	"github.com/yi-nology/jumpserver-mcp-exec/biz/exec/router"
	"github.com/yi-nology/jumpserver-mcp-exec/conf/exec"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/audit"
	"github.com/yi-nology/jumpserver-mcp-kit/pkg/reg"
)

// Version is the version of the execution-plane server.
const Version = "0.3.0"

// Main runs the command line and exits with the command's exit code.
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
		Default: "serve",
		Commands: []cli.Command{
			{Name: "serve", Summary: "以 stdio 提供 MCP 服务（默认）", Run: runServe},
			// The flag spelling is kept for compatibility: it used to be
			// this server's only spelling before it became a subcommand.
			{Name: "doctor", Summary: "检查 koko 接线（不需要 MCP 会话）",
				Aliases: []string{"--doctor", "-doctor"}, Run: runDoctor},
			{Name: "tools", Summary: "列出工具目录：名字、风险", Run: runTools},
		},
	}
	os.Exit(app.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

// serve is the default mode: MCP over stdio.
func serve(ctx context.Context) int {
	cfg, err := conf.Load()
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

	r := reg.New(&mcp.Implementation{Name: "jumpserver-mcp-exec", Version: Version}, reg.Options{
		Policy:   policy.Table,
		ReadOnly: cfg.ReadOnly,
		// The execution plane keeps the same trail the management plane
		// keeps. It matters MORE here: a DANGEROUS command refused for
		// want of a confirmation never reaches the bastion, so without
		// this line the refusal leaves no trace anywhere.
		ServerName: "jumpserver-mcp-exec",
		OnRecord:   audit.Trail(cfg.Audit.Options("jumpserver-mcp-exec")),
	})
	router.Register(r, cfg, c)
	if err := r.Serve(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "jumpserver-mcp-exec: server failed: %v\n", err)
		return cli.ExitError
	}
	return cli.ExitOK
}

// runServe is the "serve" subcommand.
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return serve(ctx)
}

// runTools is the "tools" subcommand: the catalogue, with the risk of each
// tool.
func runTools(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, err := conf.Load()
	if err != nil {
		// The catalogue does not need a bastion, and it does not need a
		// configured one either: this is the audit view of the policy table.
		//
		// ⚠️ So a broken configuration is reported but does NOT stop the
		// listing — the management plane does the same, and a server that
		// answers "I cannot list my own tools because you forgot a
		// variable" is a worse answer than the catalogue.
		fmt.Fprintf(stderr, "jumpserver-mcp-exec: %v\n", err)
		cfg = conf.Config{}
	}
	r := reg.New(&mcp.Implementation{Name: "jumpserver-mcp-exec", Version: Version}, reg.Options{
		Policy:     policy.Table,
		ServerName: "jumpserver-mcp-exec",
	})
	c := cfg.APIClient()
	router.Register(r, cfg, c)
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
