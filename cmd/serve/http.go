// 这个文件是**HTTP 面**的跑法：REST（/api/v1/…）与 MCP over streamable
// HTTP（/mcp）。
//
// ⚠️ 两个面共用同一个注册表 ⇒ 同一道风险门禁、同一套确认语义、同一份
// 留痕。HTTP 面不是后门：stdio 拒的，这里一样拒；从哪个面进来的调用，
// 都各留一行。
//
// ⚠️ 两种跑法：
//   - `serve`（默认，stdio）：JMS_HTTP_ADDR 配了就**顺带**开 HTTP ——
//     一个进程、一个门禁、一份留痕；stdio 关了进程就退，插座跟着没。
//   - `serve http`：只开 HTTP，长跑到信号为止 —— 这是当服务部署的跑法。
package serve

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/pkg/cli"

	"github.com/yi-nology/jumpserver-exec/biz/router"
	"github.com/yi-nology/jumpserver-exec/internal/config"
	"github.com/yi-nology/jumpserver-kit/pkg/httpapi"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
)

// startHTTP brings the HTTP surface up on cfg.HTTPAddr and returns the
// server, already serving in the background.
func startHTTP(cfg config.Config, r *reg.Registry, version string, stderr io.Writer) (*http.Server, error) {
	surf, err := httpapi.New(r, httpapi.Options{
		Token:    cfg.HTTPToken,
		Name:     router.ServerName,
		Version:  version,
		ReadOnly: cfg.ReadOnly,
	})
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	srv := &http.Server{Handler: surf.Handler()}
	// ⚠️ async.GoSafe，不是裸 `go func()`：裸 goroutine 没有 recover，
	// 里面的 panic 会把整个进程带走（连同留痕的写盘）—— 仓里的
	// TestNoBareGoroutines 会拦裸 goroutine。
	async.GoSafe(func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(stderr, "jumpserver-exec: http: %v\n", err)
		}
	})
	fmt.Fprintf(stderr, "listening on http://%s  (REST /api/v1/…, MCP /mcp)\n", ln.Addr())
	return srv, nil
}

// serveHTTP runs the HTTP surface until SIGINT/SIGTERM.
func serveHTTP(ctx context.Context, cfg config.Config, r *reg.Registry, version string, stderr io.Writer) int {
	if cfg.HTTPAddr == "" {
		fmt.Fprintln(stderr, "jumpserver-exec: JMS_HTTP_ADDR is required for `serve http` — where the surface listens, e.g. 127.0.0.1:8732")
		return cli.ExitError
	}
	srv, err := startHTTP(cfg, r, version, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "jumpserver-exec: %v\n", err)
		return cli.ExitError
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return cli.ExitOK
}
