# 借鉴 kael 设计四机制 + oekit 全面升级 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 spec 的四条改进（审批记忆 / 超时带部分输出 / bg 取消工具 / 五态对齐），并把 oekit、oagentkit 全家桶升到最新吃掉红利。

**Architecture:** A 落 policy 层进程内记忆组件，接线六个条件确认门；B 在 token 后端超时错误上携带有界部分输出；C/D 扩 bg 标记文件体系（pid/.started/.cancelled）并新增 jmsx_bg_cancel（工具数 18→19）。

**Tech Stack:** Go、thrift(IDL)、kit 仓 pkg/reg 门禁、oekit/oagentkit。

**Spec:** `docs/superpowers/specs/2026-11-16-kael-design-borrowings-design.md`

## Global Constraints

- 一切构建用 `./script/build.sh`（内置 `GOPROXY=off`、`-race`）；**禁止 `go mod tidy`**、禁止 `-mod=mod` 进构建、go.mod **不许 replace**。
- 依赖下载/补 go.sum 用 `GOFLAGS=-mod=mod GOSUMDB=off GOPROXY=https://goproxy.cn,direct go mod download`（只在非构建命令里带 `-mod=mod`）。
- 工具注册只能 `reg.Add`；答案必须产出契约类型（`jmsexec.*`），不手拼 map。
- 工具数口径：改动后对外 **19**（无 REST 凭据时 **18**）。
- 提交信息中文、说行为；不留构建产物；`go.work` 不入库。
- 动了失败路径 ⇒ 收尾必须起独立 verifier 子代理（kit 仓 `script/review-prompt.md` 模板），结论贴进汇报。
- 发现 kit（jumpserver-kit）代码在新依赖下编译不过 ⇒ **停下汇报**，不许在本仓 patch kit（铁律 11：先发 kit tag）。

---

### Task 1: oekit / oagentkit 全面升级

**Files:**
- Modify: `go.mod`（require 块手工同步版本，不许 tidy）
- Modify: `go.sum`（由 go mod download 生成）

**Interfaces:**
- Consumes: 无。
- Produces: 全量最新依赖树；后续所有任务在其上编译。

- [ ] **Step 1: 同步 go.mod require 到最新**

把下列版本手工改入 `go.mod`（直接 require 与 indirect 都改； indirect 标记保留原样）：

| 模块 | 旧 | 新 |
|---|---|---|
| github.com/oekit/concurrency | v0.33.0 | v0.38.1 |
| github.com/oekit/logx | v0.9.2 | v0.11.0 |
| github.com/oekit/network | v0.10.1 | v0.11.2 |
| github.com/oekit/pkg | v0.44.0 | v0.52.0 |
| github.com/oekit/bootstrap | v0.7.6 | v0.7.13 |
| github.com/oekit/core | v0.14.2 | v0.15.4 |
| github.com/oekit/kitex | v0.7.18 | v0.8.3 |
| github.com/oekit/logx/kitexlog | v0.1.11 | v0.1.12 |
| github.com/oekit/metrics | v0.3.0 | v0.4.1 |
| github.com/oagentkit/guardx | v0.23.4 | v0.28.0 |
| github.com/oagentkit/tools | v0.13.4 | v0.15.0 |

- [ ] **Step 2: 拉取模块并补 go.sum**

Run: `GOFLAGS=-mod=mod GOSUMDB=off GOPROXY=https://goproxy.cn,direct go mod download`
Expected: 无报错；`go.sum` 出现新版本的 h1 条目。若报 missing go.sum entry，
再跑 `GOFLAGS=-mod=mod GOSUMDB=off GOPROXY=https://goproxy.cn,direct go build ./...`。

- [ ] **Step 3: 全量构建 + 测试**

Run: `./script/build.sh`
Expected: 编译过、`go test ./...`（含 `-race`）全绿。
若 kit（jumpserver-kit）代码在新 oekit API 下编译失败：**停下**，按 Global
Constraints 汇报等 kit 先发兼容 tag，不许本仓 patch kit。

- [ ] **Step 4: 吃红利核对**

浏览 oekit/pkg v0.45→v0.52、oekit/network v0.11、oagentkit/guardx v0.24→v0.28
的 release notes / commits，列出与执行面相关的修复或新能力（如 shellx、sshx
的新函数），把确实用得上的在后续任务里顺手采用，用不上的在汇报里说明看过不用。

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "chore(deps): oekit/oagentkit 全家桶升到最新（pkg v0.52.0 / network v0.11.2 / guardx v0.28.0 等 11 项），红利清单见汇报"
```

### Task 2: A-1 审批记忆组件（policy 层）

**Files:**
- Create: `internal/service/policy/memory.go`
- Test: `internal/service/policy/memory_test.go`

**Interfaces:**
- Consumes: 无（纯新组件）。
- Produces: `policy.NewMemory(ttl time.Duration) *Memory`、`(*Memory).Enabled() bool`、
  `(*Memory).Grant(asset, command string)`、`(*Memory).Satisfied(asset, command string) (bool, time.Time)`、
  `(*Memory).TTL() time.Duration`、`policy.Key(asset, command string) string`。
  Task 3 的 handler 侧门禁函数只认这些签名。

- [ ] **Step 1: 写失败的测试**

`internal/service/policy/memory_test.go`：

```go
func TestMemoryGrantThenSatisfied(t *testing.T) {
    m := NewMemory(10 * time.Minute)
    if ok, _ := m.Satisfied("node-1", "rm -rf /tmp/x"); ok {
        t.Fatal("satisfied before any grant")
    }
    m.Grant("node-1", "rm -rf /tmp/x")
    ok, at := m.Satisfied("node-1", "rm -rf /tmp/x")
    if !ok || at.IsZero() { t.Fatal("grant not remembered") }
    if ok, _ := m.Satisfied("node-1", "rm -rf /tmp/y"); ok {
        t.Fatal("different command satisfied")
    }
    if ok, _ := m.Satisfied("node-2", "rm -rf /tmp/x"); ok {
        t.Fatal("different asset satisfied")
    }
}

func TestMemoryExpiry(t *testing.T) {
    m := NewMemory(50 * time.Millisecond)
    m.Grant("a", "cmd")
    time.Sleep(80 * time.Millisecond)
    if ok, _ := m.Satisfied("a", "cmd"); ok { t.Fatal("expired approval still satisfied") }
}

func TestMemoryDisabledAtZeroTTL(t *testing.T) {
    m := NewMemory(0)
    m.Grant("a", "cmd") // 不 panic
    if m.Enabled() { t.Fatal("zero TTL must be disabled") }
    if ok, _ := m.Satisfied("a", "cmd"); ok { t.Fatal("disabled memory satisfied") }
}

func TestKeyNeverContainsCommandText(t *testing.T) {
    k := Key("node-1", "rm -rf /tmp/secret-xyzzy")
    if strings.Contains(k, "secret") || strings.Contains(k, "rm -rf") {
        t.Fatal("key leaks command text")
    }
    if Key("node-1", "  rm -rf /tmp/secret-xyzzy  ") != k {
        t.Fatal("key must be insensitive to surrounding whitespace")
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/service/policy/ -run TestMemory -v`
Expected: FAIL（NewMemory undefined）。

- [ ] **Step 3: 实现 memory.go**

```go
package policy

// Memory remembers dangerous-command approvals for a short window, the
// kael Approval idea narrowed to one process: an approval granted with
// confirm=true is reusable for the SAME command on the SAME asset until
// it expires. It never stores the command text — the key is a hash, the
// command can carry secrets.
import (
    "crypto/sha256"
    "encoding/hex"
    "strings"
    "sync"
    "time"
)

type Memory struct {
    mu   sync.Mutex
    ttl  time.Duration
    hits map[string]time.Time // key → expiry
}

func NewMemory(ttl time.Duration) *Memory {
    if ttl < 0 { ttl = 0 }
    return &Memory{ttl: ttl, hits: make(map[string]time.Time)}
}

func (m *Memory) Enabled() bool { return m.ttl > 0 }
func (m *Memory) TTL() time.Duration { return m.ttl }

func Key(asset, command string) string {
    sum := sha256.Sum256([]byte(asset + "\x00" + strings.TrimSpace(command)))
    return hex.EncodeToString(sum[:])
}

func (m *Memory) Grant(asset, command string) {
    if !m.Enabled() { return }
    m.mu.Lock(); defer m.mu.Unlock()
    m.hits[Key(asset, command)] = time.Now().Add(m.ttl)
}

// Satisfied reports a live remembered approval and when it was granted.
func (m *Memory) Satisfied(asset, command string) (bool, time.Time) {
    if !m.Enabled() { return false, time.Time{} }
    m.mu.Lock(); defer m.mu.Unlock()
    exp, ok := m.hits[Key(asset, command)]
    if !ok || time.Now().After(exp) { return false, time.Time{} }
    return true, exp.Add(-m.ttl)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/service/policy/ -run TestMemory -v`
Expected: PASS ×4。

- [ ] **Step 5: Commit**

```bash
git add internal/service/policy/memory.go internal/service/policy/memory_test.go
git commit -m "feat(policy)：审批记忆组件——同资产同命令的 confirm 在 TTL 窗口内可复用（kael Approval 借鉴）"
```

### Task 3: A-2 配置项 + 六个确认门接线

**Files:**
- Modify: `internal/config/config.go`（加 `ApprovalMemoryTTL`，挨着 `PoolTTL`）
- Modify: `internal/config/config_test.go`
- Modify: `biz/handler/dryrun.go`（rehearsalM 取代 rehearsal）
- Modify: `biz/handler/exec.go`、`run.go`、`sweep.go`、`collect.go`、`bg.go`（六个调用点）
- Modify: `biz/router/register.go`、`biz/handler/transfer.go`（穿 mem）
- Test: `biz/handler/dryrun_test.go`（扩 rehearsalM 表测试）

**Interfaces:**
- Consumes: Task 2 的 `policy.NewMemory/Grant/Satisfied/TTL/Enabled/Key`。
- Produces: `rehearsalM(mem *policy.Memory, asset, command string, class jms.CommandClass, confirm bool) (riskClass string, would bool, blocked string, note string)`（dryrun.go）；六个 handler 在成功答复上填 `res.Note`（契约字段已存在，无需 IDL 改动）。

- [ ] **Step 1: 配置项 + 失败测试**

`internal/config/config.go`：在携带 `PoolTTL` 的结构里加

```go
// ApprovalMemoryTTL is how long a confirm=true on a dangerous command stays
// reusable for the identical command on the identical asset (kael-style
// remembered approval). 0 disables the memory.
ApprovalMemoryTTL time.Duration `yaml:"approval_memory_ttl" env:"JMS_APPROVAL_MEMORY_TTL" default:"10m"`
```

config_test.go 照 `TestAPIConfigReadsTheTimeoutFromTheEnvironment` 的样子加：
默认 `10m`；设 `JMS_APPROVAL_MEMORY_TTL=90s` 读出 `90s`。
Run: `go test ./internal/config/ -run TestApprovalMemory -v` → FAIL（undefined）→ 实现后 PASS。

- [ ] **Step 2: rehearsalM（先写测试）**

`biz/handler/dryrun_test.go` 加表测试：

```go
func TestRehearsalM(t *testing.T) {
    mem := policy.NewMemory(10 * time.Minute)
    dangerous := risk.Classify("rm -rf /tmp/x")
    ro := risk.Classify("ls /tmp")
    // 只读命令从不确认、不记笔记
    if rc, would, blocked, note := rehearsalM(mem, "a", "ls /tmp", ro, false); !would || blocked != "" || note != "" {
        t.Fatalf("readonly: would=%v blocked=%q note=%q", would, blocked, note)
    }
    _ = rc
    // 危险无 confirm 无记忆 ⇒ 拦
    if _, would, blocked, _ := rehearsalM(nil, "a", "rm -rf /tmp/x", dangerous, false); would || blocked == "" {
        t.Fatal("dangerous without confirm must be blocked when no memory")
    }
    // confirm=true ⇒ 放行 + 记笔记 + 授予
    _, would, _, note := rehearsalM(mem, "a", "rm -rf /tmp/x", dangerous, true)
    if !would || !strings.Contains(note, "remembered") { t.Fatalf("grant note missing: %q", note) }
    // 同命令同资产无 confirm ⇒ 记忆放行 + 笔记写明来源
    _, would, blocked, note = rehearsalM(mem, "a", "rm -rf /tmp/x", dangerous, false)
    if !would || blocked != "" || !strings.Contains(note, "remembered approval") {
        t.Fatalf("memory hit failed: would=%v blocked=%q note=%q", would, blocked, note)
    }
    // 别的资产不命中
    if _, would, _, _ := rehearsalM(mem, "b", "rm -rf /tmp/x", dangerous, false); would {
        t.Fatal("memory leaked across assets")
    }
}
```
Run: `go test ./biz/handler/ -run TestRehearsalM -v` → FAIL（undefined）→ 实现 rehearsalM 后 PASS。

- [ ] **Step 3: 实现 rehearsalM 并替换六个调用点**

dryrun.go 新增（旧 `rehearsal` 若再无调用者则删除）：

```go
// rehearsalM is the confirmation gate with the kael-style approval memory:
// confirm=true grants, a live remembered approval satisfies, everything else
// falls through to the shared risk verdict. The note it returns goes into
// the answer's note field, so the trail shows WHY a dangerous command ran.
func rehearsalM(mem *policy.Memory, asset, command string, class jms.CommandClass, confirm bool) (riskClass string, would bool, blocked string, note string) {
    riskClass = class.String()
    if !risk.RequiresConfirmation(class) { return riskClass, true, "", "" }
    if confirm {
        if mem != nil && mem.Enabled() { mem.Grant(asset, command) }
        if mem != nil {
            note = fmt.Sprintf("dangerous command confirmed; this exact command on this asset needs no further confirmation for %s", mem.TTL())
        }
        return riskClass, true, "", note
    }
    if mem != nil && mem.Enabled() {
        if ok, at := mem.Satisfied(asset, command); ok {
            return riskClass, true, "", fmt.Sprintf("confirmed via remembered approval granted at %s (expires %s)",
                at.Format(time.RFC3339), at.Add(mem.TTL()).Format(time.RFC3339))
        }
    }
    _, blocked = risk.Rehearsal(class, confirm)
    return riskClass, false, blocked, ""
}
```

六个调用点逐个改（模式相同：把 `rehearsal(cmdClass, args.Confirm)` 换成
`rehearsalM(mem, args.Asset, args.Command, cmdClass, args.Confirm)`，接住 note，
成功路径 `res.Note = jms.Opt(note)`（仅 note != "" 时））：

1. `biz/handler/exec.go:52` — 同函数内后面构造 `res` 处填 Note。
2. `biz/handler/run.go:59` — 命令列表 join 成 `strings.Join(args.Commands, "\n")` 作记忆键。
3. `biz/handler/sweep.go:219`（exec_multi）— 键同上 join；per-host 的 `HostResult` 不填（调用级事实）。
4. `biz/handler/collect.go:147`（collect）与 `:214`（collect_multi）— 键 = join(facts)。
5. `biz/handler/bg.go:53`（bg_start）— 键 = args.Command。

注意 exec.go 的 dry-run 分支（`dryRunCommand`）也要走同一判断：dry-run 命中记忆时
`would_execute=true`、note 照常——把 `dryRunCommand` 内部对 `rehearsal` 的调用
一并换成 `rehearsalM`（dryrun.go:40）。

- [ ] **Step 4: 穿 mem（router → 注册函数）**

`biz/router/register.go`：

```go
func Register(r *reg.Registry, cfg config.Config, c *jmsapi.Client) {
    mem := policy.NewMemory(cfg.Koko.ApprovalMemoryTTL) // 若字段不在 Koko 子结构则取实际所在
    handler.RegisterExecTools(r, cfg, mem)
    if c != nil { handler.RegisterInventoryTools(r, c) }
    handler.RegisterTransferTools(r, cfg, mem)
    handler.RegisterAuditTools(r, cfg.Audit)
}
```

签名变化（全部加 `mem *policy.Memory` 形参）：`RegisterExecTools`、
`RegisterTransferTools`（再透传给 `registerRunTool`、`registerCollectTool`、
`registerCollectMultiTool`、`registerExecMultiTool`、`registerBgStartTool`）。
`registerBgCheckTool` 不用记忆，保持不变。

- [ ] **Step 5: 全量测试 + Commit**

Run: `./script/build.sh`
Expected: 全绿（含 race）。注意 handler 包测试若对 note 文本有断言要同步。

```bash
git add internal/config biz/handler biz/router
git commit -m "feat：审批记忆接线——六个条件确认门接入 policy.Memory，命中/授予都写进答复 note（留痕可见）"
```

### Task 4: B — 超时错误携带部分输出（token 后端）

**Files:**
- Modify: `internal/repository/koko/delivery.go`（加 `timeoutWithOutput`）
- Modify: `internal/repository/koko/token.go`（`runText` 包装超时错误）
- Test: `internal/repository/koko/delivery_test.go`（扩）

**Interfaces:**
- Consumes: `ErrCommandTimeout`（backend.go:67）、`mergedBuffer`（backend.go:407）。
- Produces: `timeoutWithOutput(asset string, timeout time.Duration, out string) error`；
  `errors.Is(err, ErrCommandTimeout)` 与 `reg.DeliveryOf(err) == maybe_sent` 保持为真。

- [ ] **Step 1: 先查旧文案断言**

Run: `grep -rn "did not finish within\|timed out" internal/repository/koko/*_test.go`
Expected: 列出所有断言旧文案的测试——它们要在 Step 3 同步更新（分类断言
`maybe_sent`/`ErrCommandTimeout` 不变，只动文本）。

- [ ] **Step 2: 写失败的测试**

`internal/repository/koko/delivery_test.go` 加：

```go
func TestTimeoutWithOutput(t *testing.T) {
    big := strings.Repeat("a", 5000) + "TAIL-MARKER"
    err := timeoutWithOutput("node-9", 30*time.Second, big)
    if !errors.Is(err, ErrCommandTimeout) { t.Fatal("must wrap ErrCommandTimeout") }
    if reg.DeliveryOf(err) != jms.Delivery_maybe_sent { t.Fatal("delivery must stay maybe_sent") }
    s := err.Error()
    if !strings.HasPrefix(s, "execution_state_unknown:") { t.Fatalf("missing state word: %.60q", s) }
    if !strings.Contains(s, "may still be running") { t.Fatal("missing replay warning") }
    if !strings.Contains(s, "TAIL-MARKER") { t.Fatal("missing partial output") }
    if strings.Contains(s, strings.Repeat("a", 4500)) {
        t.Fatal("output must be bounded to the tail (4KiB)")
    }
}

func TestTimeoutWithOutputEmpty(t *testing.T) {
    err := timeoutWithOutput("node-9", 30*time.Second, "")
    if !errors.Is(err, ErrCommandTimeout) { t.Fatal("empty output must still classify") }
    if strings.Contains(err.Error(), "output so far") { t.Fatal("no output section when nothing captured") }
}
```
Run: `go test ./internal/repository/koko/ -run TestTimeoutWithOutput -v` → FAIL（undefined）。

- [ ] **Step 3: 实现 + 接线**

delivery.go 加（`deliveryError` 结构不动）：

```go
// timeoutWithOutput is the kael-style receipt-timeout error: the command was
// delivered and ran past the deadline, so the state is unknown, the partial
// output collected so far is kept (bounded to the last 4KiB), and the text
// still says the one sentence that decides whether a retry is allowed.
func timeoutWithOutput(asset string, timeout time.Duration, out string) error {
    text := fmt.Sprintf("execution_state_unknown: the command on %s did not finish within %s "+
        "(it may still be running on the machine — never re-run it to find out)", asset, timeout)
    if out != "" {
        bounded := out
        if len(bounded) > 4096 { bounded = bounded[len(bounded)-4096:] }
        text += fmt.Sprintf("\noutput so far (last %d bytes):\n%s", len(bounded), bounded)
    }
    return &deliveryError{text: text, delivery: jms.Delivery_maybe_sent, cause: ErrCommandTimeout}
}
```
（`fmt` 需 import。）

token.go `runText`（约 :334）改为：拿到 `err` 后

```go
out := &mergedBuffer{}
code, err := t.run(command, nil, out, out, timeout)
if err != nil && errors.Is(err, ErrCommandTimeout) {
    return out.String(), -1, timeoutWithOutput(t.asset, timeout, out.String())
}
return out.String(), code, err
```
runBinary（文件字节）**不改**——二进制尾部进错误文本是损坏文件。

Step 1 查到的旧文案断言同步更新为新文案。TUI 后端不动（屏幕重建的输出不可靠，
spec 范围就是 token 后端）。

- [ ] **Step 4: 测试 + Commit**

Run: `go test ./internal/repository/koko/ -v`（全包，确认分类断言没破）
Expected: 全绿。

```bash
git add internal/repository/koko
git commit -m "feat：超时回执带部分输出——execution_state_unknown + 4KiB 尾部，maybe_sent 分类不变（kael 回执超时借鉴）"
```

### Task 5: C+D — bg pid 标记、jmsx_bg_cancel、五态对齐

**Files:**
- Modify: `idl/bg.thrift`（pid_marker 字段 + state 文档 + 新 BgCancelResult）
- Modify: `biz/model/jmsexec/`（`./script/gen-idl.sh` 重新生成，产物入库）
- Modify: `biz/handler/bg.go`（wrapper、cancel 工具、五态映射、.started TTL）
- Modify: `internal/config/config.go` + `config_test.go`（`BgTTL`，default 0）
- Modify: `biz/handler/transfer.go`（注册 cancel）
- Test: `biz/handler/bg_test.go`（扩 builder/parser/拒答测试）

**Interfaces:**
- Consumes: `policy.Memory`（Task 3 已穿到 registerBgStartTool）；`config` 的 `BgTTL`。
- Produces: `bgCancelCommand(path string) string`、`parseBgCancelOutput(out string) (state, exitCode string)`、
  `bgCheckCommand(path string, cleanup bool, ttl time.Duration) string`（签名加 ttl）、
  五态词汇 `running/success/error/cancelled/timeout`、`jmsexec.BgCancelResult`。

- [ ] **Step 1: IDL 改动 + 重新生成**

`idl/bg.thrift`：
1. `BgStartResult` 末尾加：

```
  /** 后台进程的 pid 标记（setsid 后该 pid 即进程组组长；jmsx_bg_cancel 靠它整组终止）。 */
  10: optional string pid_marker,
```
2. `BgCheckResult.state` 注释改为：`running / success / error / cancelled / timeout（kael 五态）。`
3. 文件末尾加：

```
/**
 * 一次后台命令的取消回执（jmsx_bg_cancel）。
 *
 * state：cancelled（已终止并落 143 与 .cancelled 标记）/
 * already_finished（任务早已结束，未动它的 .rc）/ no_marker（没有 pid 标记，
 * 老任务不支持取消）/ still_running（TERM 后 5s 内仍未退出，未确认死亡）。
 */
struct BgCancelResult {
  1: optional string asset,
  2: optional string path,
  3: optional string state,
  4: optional string exit_code,
  5: optional string note,
  6: optional string risk,
  7: optional string blocked,
}
```

Run: `./script/gen-idl.sh && ./script/gen-openapi.sh`
Expected: `biz/model/jmsexec/` 与 `swagger/openapi.json` 更新，`go build ./...` 过。

- [ ] **Step 2: 先写失败的测试（bg_test.go 追加）**

```go
func TestBgStartCommandRecordsPidAndStarted(t *testing.T) {
    line := bgStartCommandForTest("/tmp/x.log") // 若现有测试已有等价 helper 就复用
    for _, want := range []string{"/tmp/x.log.pid", "/tmp/x.log.started", "echo $$ >", "setsid nohup"} {
        if !strings.Contains(line, want) { t.Fatalf("start line missing %q: %s", want, line) }
    }
}

func TestBgCheckFiveStates(t *testing.T) {
    // cancelled 优先于 .rc
    if st, _, _ := parseBgCheckOutput("CANCELLED at=1731840000"); st != "cancelled" { t.Fatalf("got %q", st) }
    if st, rc, _ := parseBgCheckOutput("FINISHED rc=0"); st != "success" || rc != "0" { t.Fatalf("got %q rc=%q", st, rc) }
    if st, rc, _ := parseBgCheckOutput("FINISHED rc=3"); st != "error" || rc != "3" { t.Fatalf("got %q rc=%q", st, rc) }
    if st, _, _ := parseBgCheckOutput("TIMEOUT"); st != "timeout" { t.Fatalf("got %q", st) }
    if st, _, _ := parseBgCheckOutput("RUNNING"); st != "running" { t.Fatalf("got %q", st) }
}

func TestBgCheckCommandCancelledAndTtl(t *testing.T) {
    line := bgCheckCommand("/tmp/x.log", false, 3600)
    for _, want := range []string{".cancelled", "CANCELLED", ".started", "TIMEOUT", "FINISHED", "RUNNING"} {
        if !strings.Contains(line, want) { t.Fatalf("check line missing %q: %s", want, line) }
    }
    if strings.Contains(bgCheckCommand("/tmp/x.log", false, 0), "TIMEOUT") {
        t.Fatal("ttl=0 must not emit the timeout branch")
    }
}

func TestBgCancelCommandLifecycle(t *testing.T) {
    line := bgCancelCommand("/tmp/x.log")
    for _, want := range []string{"kill -TERM -", "ALREADY", "NOMARK", "CANCELLED", "STILLRUNNING", "143"} {
        if !strings.Contains(line, want) { t.Fatalf("cancel line missing %q: %s", want, line) }
    }
}

func TestParseBgCancelOutput(t *testing.T) {
    cases := map[string]string{
        "CANCELLED":            "cancelled",
        "ALREADY rc=0":         "already_finished",
        "ALREADY rc=2":         "already_finished",
        "NOMARK":               "no_marker",
        "STILLRUNNING":         "still_running",
    }
    for out, want := range cases {
        if st, _ := parseBgCancelOutput(out); st != want { t.Fatalf("%q → %q, want %q", out, st, want) }
    }
    if _, rc := parseBgCancelOutput("ALREADY rc=7"); rc != "7" { t.Fatal("rc not parsed") }
}
```

Run: `go test ./biz/handler/ -run 'TestBg' -v` → 新测试 FAIL（undefined）。

- [ ] **Step 3: 实现 bg.go 改造**

3a. bg_start（bg.go:83-90 一带）：
- `marker := path + ".rc"` 处加 `pidfile := path + ".pid"`。
- wrapper 变四参（`$3` 是 pid 文件，`$$` 是 setsid 后 sh 自己的 pid——组长）：

```go
wrapper := `sh -c 'echo $$ > "$3"; sh -c "$1"; echo $? > "$2"' _ ` + shellx.Quote(args.Command) + " " + qm + " " + shellx.Quote(pidfile)
```
- start 行：`rm -f` 清单加 `.pid`、`.started`、`.cancelled`（重跑同名 path 时清掉旧标记）；
  `setsid` 前加 `date +%s > <q started>;`。
- 答复：`res.PidMarker = jms.Opt(pidfile)`；Note 追加
  `"cancel it with jmsx_bg_cancel (needs confirm=true)"`。

3b. bgCheckCommand 加 `ttl time.Duration` 形参，shell 行加两支：
- `.cancelled` 优先：`if [ -f <qc> ]; then echo CANCELLED at=$(cat <qc>);` 最先判。
- FINISHED 分支保留原样（rc 由解析层映射 success/error）。
- ttl>0 时：`elif [ -f <qstarted> ] && [ $(( $(date +%s) - $(cat <qstarted>) )) -gt <ttl秒> ]; then echo TIMEOUT;`
- 调用点（bg.go:228）传 `cfg.Koko.BgTTL`。

3c. parseBgCheckOutput 五态映射：FINISHED→rc=="0"?"success":"error"；CANCELLED→cancelled；
TIMEOUT→timeout；RUNNING→running。bg_check handler 的 note 文案随状态更新：
cancelled → `"killed by jmsx_bg_cancel (rc=143 recorded)"`；timeout →
`"still running past JMS_BG_TTL=<值>: the files are its live output; consider jmsx_bg_cancel"`；
success/error → 沿用现有「文件还在」提示（finished 字样换成实际状态值）。

3d. 新 builder/parser/handler：

```go
// bgCancelCommand 的 shell 行（顺序即判定优先级）：
// if [ -f <qrc> ]; then echo ALREADY rc=$(cat <qrc>);
// elif [ ! -f <qpid> ]; then echo NOMARK;
// else pid=$(cat <qpid>); kill -TERM -$pid 2>/dev/null; i=0;
//   while kill -0 $pid 2>/dev/null && [ $i -lt 25 ]; do sleep 0.2; i=$((i+1)); done;
//   if kill -0 $pid 2>/dev/null; then echo STILLRUNNING;
//   else echo 143 > <qrc>; echo $(date +%s) > <qc>; echo CANCELLED; fi; fi
```
⚠️ `kill -TERM -$pid` 杀的是**进程组**（setsid 保证 pgid==pid），内层 `sh -c "$1"`
是同组子孙，一起死。pid 复用窗口（任务已退出、pid 被系统重用）存在但概率极低，
且后果是 TERM 打向陌生进程组——note 里不写死，代码注释写明这个取舍。

`registerBgCancelTool`：confirm 为**无条件**门禁，照 bg_check cleanup 拒答的模式：

```go
if !args.Confirm {
    res := &jmsexec.BgCancelResult{Asset: jms.Opt(args.Asset), Path: jms.Opt(args.Path), State: jms.Opt("unknown")}
    render.FillRefusal(res, jms.Risk_write.String(),
        "set confirm=true to kill the background job on the machine")
    return render.Text(res)
}
```
通过后开会话跑 `bgCancelCommand(args.Path)`（60s 超时），按解析状态填
State/ExitCode/Note：cancelled → note `"sent SIGTERM to the process group, it exited (rc=143 recorded); output file left in place"`；already_finished → note 带 rc、明说没动 `.rc`；no_marker → note `"this job predates pid markers and cannot be cancelled"`；still_running → note `"SIGTERM sent but still alive after 5s — state unknown, do not assume it stopped"`。

3e. config.go 加（挨着 ApprovalMemoryTTL）：

```go
// BgTTL bounds how long a background job may run before bg_check reports it
// as timeout. 0 disables the timeout state (the default).
BgTTL time.Duration `yaml:"bg_ttl" env:"JMS_BG_TTL" default:"0"`
```
config_test.go 照加默认 0 / 覆盖 7200s 两例。`bgCheckCommand` 调用处传 `cfg.Koko.BgTTL`。

3f. transfer.go 的 `RegisterTransferTools` 里 `registerBgCancelTool(r, cfg)` 注册
（注意 transfer.go 现在多了 mem 形参，Task 3 已改）。

- [ ] **Step 4: 测试 + Commit**

Run: `./script/build.sh` → 全绿。

```bash
git add idl/bg.thrift biz/model/jmsexec biz/handler/bg.go biz/handler/transfer.go internal/config swagger
git commit -m "feat：后台命令可取消（jmsx_bg_cancel）+ bg 五态对齐 kael——pid/.started/.cancelled 标记体系"
```

### Task 6: policy 表、工具数、文档同步

**Files:**
- Modify: `internal/service/policy/policy.go`（Table 加 jmsx_bg_cancel）
- Modify: `internal/service/policy/policy_test.go`（want 名单 +1）
- Modify: `README.md`、`docs/CONFIGURATION.md`、`skills/`、`AGENTS.md`、`CHANGELOG.md`

**Interfaces:**
- Consumes: Task 5 的 `jmsx_bg_cancel`（已注册）。
- Produces: 工具数口径 **19（无凭据 18）** 的全仓一致表述。

- [ ] **Step 1: policy 表 + 测试**

policy.go Table「Acts」段加：

```go
// Cancelling kills a live process on the machine, so it is a write like
// any other — unconditional confirm, like put_file.
"jmsx_bg_cancel": {Risk: jms.Risk_write,
    Confirm: "set confirm=true to kill the background job on the machine"},
```
policy_test.go 的 want 名单加 `"jmsx_bg_cancel"`（计数断言 want/want+1 不用动）。
Run: `go test ./internal/service/policy/ -v` → PASS。

- [ ] **Step 2: 文档同步（逐处改）**

- `AGENTS.md` §3.1 工具列表加 `jmsx_bg_cancel`，「18 个工具」→「19 个」、
  「17」→「18」（两处口径都改，含 §0 目录树的 18 个工具注释）。
- `README.md` 工具表加一行；安全模型节提到审批记忆（JMS_APPROVAL_MEMORY_TTL）。
- `docs/CONFIGURATION.md`：环境变量表加 `JMS_APPROVAL_MEMORY_TTL`（默认 10m）
  与 `JMS_BG_TTL`（默认 0）；后台命令一节加取消流程与五态说明。
- `skills/`（jms-exec-ops 工具表）：加 `jmsx_bg_cancel`，并把 bg_check 的
  state 词汇改成五态。
- `CHANGELOG.md`：[Unreleased] 加 Added 段（jmsx_bg_cancel、审批记忆、
  超时部分输出、bg 五态）与 Changed 段（依赖升级）。

- [ ] **Step 3: 全量验证 + Commit**

Run: `./script/build.sh && ./script/gen-openapi.sh && go test ./... -count=1`
Expected: 全绿；`openapi_test.go` 逐字节比对过。

```bash
git add internal/service/policy README.md docs skills AGENTS.md CHANGELOG.md
git commit -m "feat：policy 表收录 jmsx_bg_cancel，工具数 18→19，文档与 skills 同步"
```

### Task 7: 全量验证 + verifier + 真调

**Files:** 无新改动（验证任务）。

**Interfaces:**
- Consumes: Task 1-6 全部产出。
- Produces: 交付汇报（含 verifier 结论）。

- [ ] **Step 1: 构建 + 自检**

Run: `./script/build.sh`
Expected: 编译零错、单测（含 -race）全绿、产物 `./bin/jumpserver-exec`。
Run: `./bin/jumpserver-exec --doctor`
Expected: 退出码 0。
汇报要素之一：工具数（从 `tools` 子命令或 policy 测试确认 19）。

- [ ] **Step 2: 铁律 8 verifier 子代理**

起一个**独立 verifier 子代理**（未参与以上任何实现），提示词用 kit 仓
`script/review-prompt.md` 模板，重点额外列：
1. 超时错误新文案是否在所有路径保持 maybe_sent、有没有哪条路径丢了这个分类；
2. 审批记忆有没有绕过只读模式 / 无条件确认工具的口子；
3. cancel 的 kill -TERM -pid 进程组语义与 pid 复用风险注释是否属实；
4. 五态映射有没有把「仍在跑」误报成终态的口子；
5. 工具数口径 19/18 与 policy 集合、swagger、skills 是否一致。
子代理长时间零输出（>15 分钟）按 AGENTS.md 退路：改按审阅清单逐条自己核。
**审阅结论逐条贴进最终汇报**（修了哪几条、哪几条判不属实）。

- [ ] **Step 3: 发布 + 真调**

```bash
python3 script/publish-mcp.py
mcp update
```
`mcp get jms-exec` 看 command 指向新运行时目录。真调（env 从
`~/.minimax/mcp.json` 的 jms-exec.env 注入，shell 里没有 JUMPSERVER_*）：
1. `jmsx_exec` 跑一条 `sleep 30 && touch /tmp/jmsx-verify` 带 timeout=5 ⇒
   错误里要看到 `execution_state_unknown` + 空输出段，且分类 maybe_sent；
2. `jmsx_bg_start` 起 `sleep 300` ⇒ 答复含 pid_marker；
3. 同一条 DANGEROUS 命令（如对 /tmp 的 rm）先 confirm=true 跑、再不带
   confirm 重跑 ⇒ 第二次命中记忆放行且 note 写明 granted at；
4. `jmsx_bg_cancel`（confirm=true）取消该 sleep ⇒ state=cancelled；
5. `jmsx_bg_check` ⇒ 五态 cancelled；rc=143。

- [ ] **Step 4: 汇报**

按 AGENTS.md §4 口径汇报：编译结果、测试结果（含 -race）、工具数变化
（18→19 / 17→18）、发布路径、自检结果、verifier 结论、真调记录，
以及「**需要重启会话才生效**」。oekit 红利清单（Task 1 Step 4）一并给出。

## Self-Review 记录

- spec 覆盖：A=Task 2+3，B=Task 4，C=Task 5（3a/3d）+6，D=Task 5（3b/3c/3e）；
  契约改动=Task 5 Step 1（比 spec 预想少：A 的 note 字段契约里已存在）；
  verifier=Task 7 Step 2；文档=Task 6。无缺口。
- 类型一致：rehearsalM 签名在 Task 3 定义并全部被调用点引用；bgCheckCommand
  新 ttl 形参在 Task 5 Step 2 测试与 3b/3e 一致；BgCancelResult 字段在 IDL 与
  handler 填充处一致（asset/path/state/exit_code/note/risk/blocked）。
- 已消除占位符：所有代码步骤含完整代码；跨模块接线点给了确切文件行号区间。
