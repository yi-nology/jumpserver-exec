# jumpserver-mcp-exec

MCP server that lets an AI agent run commands on machines managed by a
JumpServer bastion — **always through the bastion**, never by connecting to the
host directly.

Every session lands in JumpServer's audit trail with a replayable recording. That
is the entire point: if it matters enough for an agent to touch a machine, it
should matter enough to be recorded.

## Why this is not just an `ssh` wrapper

JumpServer's koko terminal (SSH on port **2222**) only speaks a full-screen TUI.
An agent cannot type into a TUI, so this server drives it:

```
ssh → koko 2222 → Ctrl+U → type asset → Enter → Enter → shell prompt → command
```

Three things had to be discovered empirically, and all three are load-bearing:

1. **ConnectionToken is a dead end.** `authentication.models.ConnectionToken`
   looks like the natural automation handle, but koko rejects it as an SSH
   credential (`Permission denied` both as username and as password). It is for
   the official `jumpserver-client`, not for OpenSSH. macOS `ssh` also has no
   `host:asset` syntax. Driving the TUI is the only route.

2. **A PTY with no window size never renders.** koko embeds tmux; a 0x0 window
   yields a blank screen. 50x220 works.

3. **tmux destroys the output.** Two separate problems, both solved in `Exec`:
   - tmux packs several short lines onto one physical row, so a 5-line result
     arrives as one scrambled line. Fixed by piping through `cat -n` and
     reassembling by line number.
   - tmux renders the tab that `cat -n` uses as a separator at **zero width**, so
     the screen shows `2node` with the separator gone. Fixed with
     `| tr '\t' '@'`.

## Tools

Tools that run a command ask for `confirm=true` **only when the command is
classified DANGEROUS** — a recursive delete, a reboot, a user deletion, a
privilege change. A routine sweep (`df`, `ps`, `uptime`) needs no flag; a
destructive command that arrives without one is not executed, and the tool
answers with the plan it did not carry out (`classified DANGEROUS; re-call
with confirm=true to execute`).

Every command tool also takes `dry_run=true`: it opens no session and runs
nothing, it only reports how the command was classified and **whether it would
run** (`would_execute`). A destructive command without the confirmation comes
back with `would_execute: false`; with it, `true`. So the question can be
asked before the fact, at no cost.

A missing `would_execute` means **"not said"**, not "it would not have run".
Only the tools that can answer the question carry the field, and the trail
records it only when the answer said it.

The two multi-host tools also **price the sweep** in that answer.
`jmsx_collect_multi` and `jmsx_exec_multi` add a `plan`: how many **rounds**
the sweep would take (the waves of hosts at the given concurrency), roughly
how many seconds it would cost, and what raising the concurrency would buy.
The numbers behind it are measured on this bastion — the TUI walk and the cost
of a command are timed as they happen, and `jmsx_session_cost` measures the
same walk — and the answer says which numbers it used and whether they were
measured or are the documented defaults.

The classification is the shared one in `pkg/risk` — the same
classifier the management plane's `jms_job_run` uses, so the two sides of the
bastion cannot drift apart. `jmsx_put_file` is the exception: it overwrites a
file on the machine, so it always asks. The read-only tools
(`jmsx_collect`, `jmsx_collect_multi`, `jmsx_get_file`, `jmsx_reachable`,
`jmsx_bg_check`, `jmsx_inventory`, `jmsx_session_cost`) never ask.

| Tool | Purpose |
|---|---|
| `jmsx_exec` | Run a shell command on an asset; returns stdout+stderr and the exit code. Pass `full=true` to get output the pane could not show |
| `jmsx_run` | Several commands on **one** session, skipping the repeated TUI walk (measured: 4 commands in 24s vs ~40s as 4 execs) |
| `jmsx_collect` | Several facts from **one** session, returned as a labelled map instead of a positional list |
| `jmsx_exec_multi` | The same command on many hosts, each in its own recorded session; `dry_run=true` prices the sweep before it runs |
| `jmsx_collect_multi` | The same facts from many hosts, returned as a matrix (row = host, column = fact); `dry_run=true` prices the sweep before it runs |
| `jmsx_put_file` | Upload a file: base64 chunks, **size verified after the write**, sha256 reported |
| `jmsx_get_file` | Download a file in base64 chunks; reads back what one screen cannot hold at once |
| `jmsx_bg_start` / `jmsx_bg_check` | Long work, detached from the session: start it, then poll its state and exit code |
| `jmsx_reachable` | Check that the bastion can open a shell on an asset |
| `jmsx_inventory` | List assets with address, platform, connectivity, protocols |
| `jmsx_session_cost` | Quantify what a session costs and what it saves |

**A session does not carry shell state.** Every command runs in a pipeline subshell,
so `cd` / `export` do not survive into the next command. Write what depends on
state as one command: `cd /etc && pwd`.

Upload and download are round-trip safe: multi-space runs and newlines survive
(`'  indented  and   multi-space  '` comes back byte-identical), because spaces
are carried as 0x1E across the terminal and restored on the way out.

## Configuration

| Env var | Default | Meaning |
|---|---|---|
| `JMS_BASTION_HOST` | *(none — required by the koko terminal backend)* | JumpServer host |
| `JMS_KOKO_USER` | *(none — required by the koko terminal backend)* | JumpServer account with an SSHKey registered |
| `JMS_KOKO_KEY` | `~/.ssh/id_rsa` | Private key for that account |
| `JUMPSERVER_URL` | *(none)* | For `jmsx_inventory` and the connection-token backend |
| `JUMPSERVER_TOKEN` | *(required for inventory)* | JumpServer PrivateToken |

⚠️ The two `koko` variables have **no default**: a default would point one
deployment's clients at somebody else's bastion. With `JMS_BACKEND=auto` they are
only needed for the terminal fallback — the connection-token backend talks to
the REST API instead. `jumpserver-mcp-exec --doctor` says which of the two you are in.

The koko account must be registered in JumpServer's `SSHKey` table (v5 keeps
user public keys there, **not** in the legacy `User.public_key` field), must use
an **RSA** key, and must hold an asset permission whose `accounts` is
`["@ALL"]` — an empty `accounts` list means *no* accounts, not *all* accounts.

## Behaviour worth knowing

- **No privilege escalation.** The command runs as the asset account. Prefix
  `sudo -n` for root. Most lab accounts will refuse without a password, and that
  refusal is reported as normal output.
- **`exit_code: -1` means unknown**, not a status. It happens when the command
  kills the session (a bare `exit`) or the output cannot be parsed. Wrap the
  command in a subshell: `sh -c '...'`. The tool adds a `warning` field in this
  case.
- **~10s per call.** The TUI walk is sequential; there is no session reuse yet.
- **Web/DB assets have no shell.** `jmsx_exec` on a Website asset fails with a
  no-shell-prompt error, which is correct.

### Output is capped at ~40 lines, and says so — `full=true` lifts it

The PTY is 50 rows tall and koko's tmux only paints what fits, so a long result
loses its top. Measured on node-071, 2026-10-15:

| command | `truncated` | lines returned |
|---|---|---|
| `seq 1 10` | absent | 10 (complete) |
| `seq 1 35` | absent | 35 (complete) |
| `seq 1 200` | `true` | 89, starting at 112 |

The flag matters more than the output. A silently shortened answer is worse than
no answer, because an agent cannot tell that rows 1-4 were never seen.

**`full=true` gets all of it.** Every command is `tee`'d to a remote temp file
before the numbering pipeline mangles it, so when the pane came back short the
complete result is read back in ~4.8 KB base64 chunks — verified 200/200 and
1000/1000 lines, byte-exact and in order.

The command is **not re-run** to fetch its output: a command that appends,
deletes or otherwise has side effects would then happen twice. The re-read comes
from the artefact of the run that already happened. Cost is one round trip per
chunk, so narrow first and use `full=true` when you actually need the whole thing.

### ~~Known limit: commands slower than a few seconds~~ — not real

This section used to claim that a command slower than a few seconds comes back
empty. **Measured 2026-10-15: that is false.** Latency with fixed small output:

| command | wall time | result |
|---|---|---|
| `sleep 3; echo MARK3` | 14.3s | complete |
| `sleep 5; echo MARK5` | 15.6s | complete |
| `sleep 8; echo MARK8` | 18.7s | complete |
| `sleep 12; echo MARK12` | 22.9s | complete |
| `sleep 5` + 100 lines | 14.9s | complete |

The `probeResult` poll in `Exec` waits for the payload to materialise rather
than trusting the clock, which is why the pipe buffering does not bite. The real
constraint is **rows, not time**. Anything this README previously said about
slow commands should be read as obsolete.

### What the terminal cost actually is (supersedes an older claim)

An earlier version of this README told you to avoid slow commands. That was
measured to be **wrong**, and the mechanism behind it is worth stating so it is
not "fixed" again by someone chasing it:

The result is piped through `cat -n` to survive tmux's side-by-side row packing,
and a pipe buffers until the command exits — so in principle the payload reaches
the screen *after* the command is over. The reason that turned out not to bite
is `probeResult`: `Exec` does not trust the clock once the end marker appears, it
polls until the numbered rows actually show up. Hence `sleep 12` returning
complete in 22.9s.

The looser end-marker match is deliberate. Requiring the marker to start a line
of its own — so the terminal's echo of the command line is not mistaken for the
real marker — was tried and **reverted**: once tmux packs rows the marker does not
reliably begin a line, the stricter test never fired, and every call timed out.

So:

- **`jmsx_exec` is fine for slow work.** Time is not the constraint; rows are.
- **`full=true` is the answer for big output**, not a shorter command.
- **`jms_job_run`** goes through ansible so it has no pane limit, but v5 exposes
  no stdout for it at all.
- **Do not fall back to the command log.** See below — it is not recording.

### The command log is NOT a usable fallback (2026-10-15, measured)

An earlier README recommended reconstructing a session from `/terminal/commands/`.
**Measured coverage: 1 of the last 84 sessions carries any record**, and that one
record is the session's *first* command — everything after it is dropped. The only
larger batch is from three hours ago and contains terminal fragments (`t`, `e`,
`it`). A real `ssh -p 2222` client behaves the same, so this is not an artifact of
this client, and restarting `jms_koko` does not restore it.

Ruled out: retention cap (382 rows over six months), a settings toggle (the
settings table has no such key), celery (healthy), the access key (created in
April), DB writes (a manual insert succeeds), koko errors (the log has only EOF).

Two separate mistakes produced a wrong answer here before, both worth avoiding:

- The test asserted *"a record containing our marker exists"* rather than *"this
  run was recorded"*, and matched a marker left in history three hours earlier.
- `/terminal/commands/?search=` filters the **typed input**, not the output.
  Searching for a marker that only exists in the output finds nothing and looks
  exactly like "recording is off".

Use `full=true` instead — it reads the artefact of the run that actually
happened, so it cannot inherit this failure.

## Structure

This plane's place in the repository (hz's standard layout, the service's part):

```
biz/exec/               ← this plane (handler / model / policy / router)
conf/exec/              ← koko wiring and the REST endpoint
idl/jmsexec/            ← the contract (thrift): command results / session
                          #   statistics / sweep estimates / transfer results
internal/exec/koko/     ← the koko TUI protocol and the file transfer
cmd/jumpserver-mcp-exec/       ← the command line: serve / doctor / tools / version
```

The contract vocabulary is this plane's own — `idl/jmsexec/` (the entry
`jmsexec.thrift` includes the per-domain files) generates
`biz/exec/model/jmsexec/` — and this plane's own code depends on
it. The shared base (`pkg/`) depends on neither plane: a base that reaches
into one service stops being shared.

The gate is shared with the management plane and lives in `pkg/reg` — the same
module, so there is nothing to resolve next door, so there is no published
version and no version lie in any go.mod.

## Build

```bash
go build -o jumpserver-mcp-exec .
go test ./...
```

`go mod tidy` does **not** work in this sandbox (it tries to fetch test-only
deps, the module cache blocks the rename, and in workspace mode it would look
for the unpublished `jms-kit` on the proxy). The `replace` directives in
`go.mod` point at the extracted copies in GOPATH.

Builds run in **module mode** (one `go.mod` at the repository root), so `-mod=mod`
is not accepted — the workspace only takes `readonly`.

## Tests

`koko_test.go` and `koko_wide_test.go` cover the parsing layer against
screenshots taken from the real koko display, including the side-by-side column
layout, frame decoration, wide characters and the glued `2node` form that tmux
produces when a tab collapses. These cases are the reason the output layer is
correct; they are cheap to run and catch regressions that a single end-to-end
happy path would miss.
