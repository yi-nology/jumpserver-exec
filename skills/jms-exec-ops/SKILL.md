---
name: jms-exec-ops
description: |
  Run commands and move files on machines through the JumpServer bastion. Use when a
  request involves 在某台机器上执行命令 / 批量下发命令到多台 / 查磁盘内存日志 / 健康巡检 /
  机器负载 / 网络连通性排障 / 传文件 / 下载文件 / 跑长任务 / 部署脚本 / 查本机执行留痕（谁试过什么、被谁拦下）, or when a jmsx_*
  tool is available. Covers session
  cost, parallel fan-out, detached long jobs, the ~40-row output ceiling and how to get
  past it, and the koko TUI protocol's current quirks. Two ready recipes: fleet health
  check and machine-vantage connectivity troubleshooting.
descriptions:
  zh-Hans: "通过堡垒机在受管机器上执行命令与传文件：单条/多条/多台并发/长任务后台、文件上传下载取段、大输出截断处理、本机执行留痕查询。涉及连机器执行命令、批量下发、查磁盘日志、传文件、部署、健康巡检、查机器负载、连通性排障、查「谁试过什么被拦下了」时使用。"
displayNames:
  zh-Hans: "堡垒机执行面"
---

# 在受管机器上干活

**一切经堡垒机 koko(2222)，会话有录像。** 不接受直连 SSH。

---

## 1. 选工具

<!-- tools:exec -->
| 场景 | 工具 |
|---|---|
| 一条命令 | `jmsx_exec` |
| 多条命令 / 有依赖关系 | `jmsx_run` |
| 采集多个事实（要带标签返回） | `jmsx_collect` |
| 多台机器同一条命令 | `jmsx_exec_multi`（默认并发 4） |
| 多台机器采同一组事实 | `jmsx_collect_multi`（矩阵：行=机器，列=事实） |
| 超过 10 分钟的长任务 | `jmsx_bg_start` + `jmsx_bg_check` + `jmsx_get_file` |
| 上传文件 | `jmsx_put_file`；整目录用 `jmsx_put_dir`（tar 流） |
| 下载文件 / 只取一段 | `jmsx_get_file`（`offset` / `length`） |
| **下载整个目录** | `jmsx_get_dir`（tar 流，与 `jmsx_put_dir` 对称） |
| **一台机器的文件直接传到另一台** | `jmsx_relay`（字节不经过本机） |
| **一个目录从一台机器推到另一台** | `jmsx_relay_dir`（tar 管道对拷，不落本机） |
| 能不能连上 | `jmsx_reachable` |
| **机器到它的依赖通不通**（应用排障） | 暂无工具，用连通性排障配方：[`references/connectivity.md`](references/connectivity.md) |
| 想知道连接池里现在有什么 | `jmsx_pool_status` |
| 想先算一次会话划不划算 | `jmsx_session_cost` |
| 不知道资产叫什么 | `jmsx_inventory` |
| **查「谁在这里试过什么、被谁拦下了」** | `jmsx_audit_query`（本机门禁留痕，堡垒机上查不到） |
<!-- /tools:exec -->

（回查这些传输在堡垒机侧的记录用管理面的 `jms_file_transfers`——谁传的/哪个文件/成没成；它是另一个 server 的工具，不属于上面的清单。）

**危险命令要 `confirm=true`**：命令被判定为 **DANGEROUS**（递归删除、重启、删用户、
`mkfs`、提权……）时必须带 `confirm=true`，否则**命令不会执行**，工具回你一句
`classified DANGEROUS; re-call with confirm=true to execute`，你补上标志再来一次即可。

日常巡检命令（`df`、`ps`、`uptime`、`id`、`systemctl status`、`docker ps`……）
**不用**带标志 —— 它们被判为 READ（真 shell AST 解析 + 只读命令表判的，
`echo "rm -rf /"` 这种把危险词当参数的命令也算 READ）。

⚠️ **`jmsx_collect` / `jmsx_collect_multi` 也要看**：它们的 `checks` 里是**你给的命令**，
所以它们和 `jmsx_exec` 一样按命令判风险 —— 里面有一条危险命令，整次调用就停。

`jmsx_put_file` 和 `jmsx_relay` 是例外：前者覆盖远端文件、后者在目标机器上写文件，
一律要 `confirm=true`。
只读的那些工具（`jmsx_get_file` / `jmsx_reachable` /
`jmsx_inventory` / `jmsx_session_cost` / `jmsx_audit_query`）不用。

**先看再跑**：`jmsx_exec` / `jmsx_run` / `jmsx_collect` / `jmsx_collect_multi` /
`jmsx_exec_multi` 都支持 `dry_run=true` —— 不开会话、不执行任何东西，只告诉你这条命令被判成什么、
**会不会执行**（`would_execute`）。危险命令不带 confirm 时
`would_execute=false`，带上就是 `true` —— 先看这个再决定要不要跑。
⚠️ 答复里**没有** `would_execute` 这一格 = **没说**，不是「不会跑」——
只有答得出来的工具才会给这一格，别把「没这个字段」读成 `false`。

**多台的那两个工具还会报「这套要多久、几轮」**：`jmsx_collect_multi` /
`jmsx_exec_multi` 的演练答复里带一个 `plan`（轮数、预计秒数、并发拉满能省多少），
底数是**实测的**握手与命令耗时 —— 详见下面第 3 节。

⚠️ **计划里的 `walk_seconds` 是「新开一条连接」的成本，`pooled_walk_seconds` 是
「复用已有连接」的成本，`pooled_hosts` 是这次有几台已经连过了。**
重复扫同一批机器时，那几台几乎免费 —— 所以别拿一次 sweep 的数字去套下一次。

---

## 2. 会话成本（为什么不该一个命令一个调用）

**开会话有两种路径，代价差着六七倍。** 答复里的 `via` 会写明这次走了哪条，
`stats.backend` 是它的短名。

| | 连接令牌（默认） | koko 全屏终端（兜底） |
|---|---|---|
| 开会话 | 实测 **0.7~0.9s**（2026-10-28） | 实测 **7.25~9.12s**（2026-10-26，**会波动**） |
| 一条命令 | 实测 0.15~0.25s | 实测约 1.5s |
| 退出码 | **原生**（`exit 42` 就是 42） | 自己解析屏幕上的标记，命令杀了 shell 时是 `-1` |
| 大输出 | **完整**（实测 134KB / 4000 行不截断） | 受约 40 行窗格限制，`truncated: true` |

- 令牌路径走 `POST /api/v1/authentication/connection-token/`，再用令牌当 SSH 凭据
  直连 koko 2222（用户名 `JMS-{token id}`）—— 不走资产面板，所以快。
- **令牌是单次的**：签一次只能用一次连接；连上以后一条连接可以开多条通道。
- 只有令牌**机制**不可用时（端点不存在、部署不提供）才回退终端。
  资产本身的问题（没账号、没权限）**不回退** —— 终端也会以同样的理由失败，只是慢十秒。
- `jmsx_run` 一次会话跑多条命令 ⇒ 只付一次开会话的开销
- `jmsx_collect` 一次会话采集多个事实 ⇒ 返回**带标签的 map**，比 `jmsx_run` 的
  位置列表更不容易读错

`jmsx_session_cost` 能实测这次开会话花了多久（`stats.walk_seconds`）——
**现在这个数字是真量出来的**，不再是写死的估值；多机演练的 `plan` 用的就是同一份测量。

---

## 3. 多台机器：并发

```bash
jmsx_exec_multi {"assets":["node-031","rpi4-011","dnsbox1","node27"],
                 "command":"df -h / | tail -1","concurrency":4}
```

- 每台机器一条**独立录像会话**，所以并发不会串数据。
- 实测 6 台：串行 54.9s → **并发 25.5s**。
- `concurrency` 可调 1~8，默认 4。
- 某台连不上**不会中断整批**，它会被报成 failed。
- `stop_on_error=true` 时不再派发新主机，已派发的跑完，未派发的标 `skipped`。

⚠️ `jms_job_run` 也能批量下发，但 **v5 的 API 不返回 job 的执行输出**，
要每台的输出就必须用 `jmsx_exec_multi`。

---

### 批量采集要矩阵，不要 N 次调用

```bash
jmsx_collect_multi {"assets":["node-031","rpi4-011","dnsbox1"],
  "checks":[{"name":"host","command":"hostname"},
            {"name":"disk","command":"df -h / | tail -1"}]}
```

返回 `matrix`：**行是机器、列是事实**。
每台机器**一条会话**（所以一台的事实不会串到另一台），机器之间并发。

这是 fleet 运维的主力场景：同一组问题问遍所有机器，答案要能并排看。

**标准巡检配方**（负载/内存/磁盘/TCP/进程的检查集、判读阈值、CLOSE-WAIT 应用 bug 信号）：
[`references/health-check.md`](references/health-check.md) —— 每条命令实测免确认，照抄即可。

### 大范围采集：先演练，问清楚「几轮、多久」

```bash
jmsx_collect_multi {"assets":["node-031", ... 共 20 台],
  "checks":[{"name":"disk","command":"df -h / | tail -1"},
            {"name":"up","command":"uptime"}],
  "dry_run":true}
```

演练答复里除了风险分级，还带一个 `plan`（下面是**示例**，秒数随实测变）：

```json
{"dry_run":true,"would_execute":true,"risk":"READ",
 "plan":{"hosts":20,"sessions":20,"commands_per_host":2,
         "concurrency":4,"rounds":5,
         "estimate_seconds":60.2,"estimate":"1m",
         "min_seconds":45.6,"max_seconds":645.6,
         "walk_seconds":9.1,"command_seconds":1.5,
         "walk_source":"measured on this bastion (median of 1 walk(s))",
         "command_source":"measured on this bastion (median of 1 command(s))",
         "at_max_concurrency":{"concurrency":8,"rounds":3,"estimate":"36s"}}}
```

- `rounds` = **几轮**：每轮开 `concurrency` 条会话，20 台 / 并发 4 = **5 轮**。
- `estimate_seconds` = **大概多久**（估算，不是承诺）。
  `min_seconds` 是地板（只算握手），`max_seconds` 是天花板（每条命令都跑到超时）。
- `at_max_concurrency` = **并发拉满能省多少**（同样 20 台，并发 8 → 3 轮）。
- `sessions` = 会开出多少条录像会话（每台一条）。
- 底数**是实测的**：握手和单条命令的耗时都随会话真量，取中位数；
  本进程还没量过时用文档默认值，并且 `walk_source` 会写明是默认值。

⚠️ 演练**不开会话**，所以它报的是"按已测成本推算"，不是"这台机器上跑过"。
想看某台的真实成本，就用它跑一次 `jmsx_session_cost`。

## 4. 长任务：后台化

```bash
# 启动，立即返回
jmsx_bg_start {"asset":"192.0.2.71","command":"./deploy.sh","path":"/tmp/deploy.log"}
# → {"output_path":"/tmp/deploy.log","exit_marker":"/tmp/deploy.log.rc"}

# 轮询
jmsx_bg_check {"asset":"192.0.2.71","path":"/tmp/deploy.log"}
# → 跑完后垃圾还在线上；读完输出带 cleanup=true + confirm=true 一次清掉
jmsx_bg_check {"asset":"192.0.2.71","path":"/tmp/deploy.log","cleanup":true,"confirm":true}
# → {"state":"running"} 或 {"state":"finished","exit_code":0}

# 读输出
jmsx_get_file {"asset":"192.0.2.71","remote_path":"/tmp/deploy.log"}
```

进程是**脱离会话**的（`setsid`），所以堡垒机关掉连接不会杀掉它。
退出码写在 `.rc` 标记文件里，**靠它区分「还在跑」和「跑完了」** ——
光看输出文件存在是判断不出来的。

---

## 5. 读结果：三个字段要一起看

| 字段 | 含义 |
|---|---|
| `exit_code` | **`-1` = 未知**，不是状态码。命令杀了 shell（裸 `exit`）或输出无法解析时出现 ⇒ 改用 `sh -c '...'` 包一层 |
| `truncated` | **`true` = 输出超过终端高度（约 40 行），开头被截掉了**。只在走了终端兜底路径时才会出现 —— 令牌路径的输出是完整的 |
| `output` | 去掉行号、按序拼接的正文 |

⚠️ **`truncated: true` 时绝不能假设你看到了全部内容** —— 这是最容易产生误判的地方。
处理顺序：

1. `jmsx_exec` 加 `full=true` —— 面板返回不全时，自动从远端 tee 好的临时文件
   分块取回完整结果，**不重跑命令**（重跑对有副作用的命令是错的）。
2. 执行前就收窄：`| head -50`、`| grep -c`、`| tail -20`、`--no-pager`。
3. 统计先于详情：先 `wc -l` 看规模，再决定怎么取。
4. 超大文件用 `jmsx_get_file`（也是分块的）。
5. 文件要从一台机器到另一台，用 `jmsx_relay` —— 字节从源端通道直接进目标端通道，
   **不落本机磁盘**，比「下载再上传」少一次搬运，也少一份中间副本。

---

## 6. 传文件

```bash
# 从本机传
jmsx_put_file {"asset":"node-071","local_path":"/tmp/deploy.sh","remote_path":"/opt/app/deploy.sh","mode":700,"confirm":true}

# 整目录一次传（合并语义：同名覆盖、远端已有保留；要整树镜像先清目录）
jmsx_put_dir {"asset":"node-071","local_path":"/tmp/app-bundle","remote_path":"/opt/app","confirm":true}

# 内容已经在手上（不必先落本地文件）
jmsx_put_file {"asset":"node-071","remote_path":"/opt/app/conf.yaml","content":"key: value\n","confirm":true}
jmsx_put_file {"asset":"node-071","remote_path":"/tmp/x.bin","content_base64":"<base64>","confirm":true}

# 只取远端文件的一段（窗口在远端应用，不搬整文件）
jmsx_get_file {"asset":"node-071","remote_path":"/var/log/app.log","offset":0,"length":4096}

# 一台机器的文件直接传到另一台：字节不经过本机
jmsx_relay {"source":"node-071","source_path":"/opt/app/conf.yaml",
            "destination":"rpi4-011","destination_path":"/opt/app/conf.yaml","confirm":true}
```

- 目标目录**不存在会自动创建**。
- 传完会**比对 sha256**：`checksum_match: true` 才说明内容也是对的
  （只比大小的话，同样长度但内容被改写是查不出来的）。
- 三个来源**只能给一个**（`local_path` / `content` / `content_base64`）。

---

## 7. 读结果拿不到时的回退

命令记录**开着但覆盖率极低**（2026-10-15 实测：最近 84 个 session 只有 1 个带记录，
且只记了第一条），**不能当审计通道，也不能当大输出的回退通道**。

要原始终端流用**录像**：

```bash
jms_session_replay {"session_id":"<id>"}   # 走 /terminal/sessions/{id}/replay/download/
```

---

## 8. 执行前先预检

堡垒机可以配**命令过滤器**，拦掉危险命令。跑之前先查，能省一次注定失败的会话：

```bash
jms_command_guard {"command":"rm -rf /data","asset":"192.0.2.71","user":"ai"}
# → {"status":"fail","verdict":"the bastion will REFUSE this command: no-rm rejects \"rm\"",
#    "advice":"...别直接跑，要么换个做法，要么让运维改过滤器作用域",
#    "checks":[{"name":"filter:no-rm","status":"fail","detail":"...","fix":"..."}],
#    "matching_filters":[{"filter":"...","action":"reject","matched_command":"rm"}]}
```

`status` 才是机器可读的结论：`fail`=会被拒，`warn`=会记一笔并提示，
`ok`=没有过滤器拦它（或有 accept 规则）。

匹配规则跟堡垒机一致：`type=command` 的命令组按**独立词**匹配
（`rm` 命中 `rm -rf` 和 `sudo rm`，**不**命中 `rmdir`），`type=regex` 走正则。
过滤器还要**作用域对得上**（users / assets / accounts）才算命中。

---

### 本机留痕：被拦下的东西，堡垒机那边查不到

门禁是在**本机**判的。危险命令没带 `confirm=true` 就不会执行，而它**根本没到堡垒机** ——
所以堡垒机的会话列表、审计、`jms_command_audit` 里**什么都没有**。
本机每个进程留一份痕（`~/.local/state/jumpserver-mgmt/audit/audit-*.jsonl`），要查就用：

```bash
jmsx_audit_query {"refused":true,"since":"2026-10-28"}
# → {"trail_dir":"/Users/…/.local/state/jumpserver-mgmt/audit",
#    "entries":[{"ts":"2026-10-28T09:12:03+08:00","service":"jumpserver-exec","tool":"jmsx_exec",
#                "args":"{\"asset\":\"node-071\",\"command\":\"rm -rf /var/lib/x\"}",
#                "allowed":false,
#                "refused":"classified DANGEROUS; re-call with confirm=true to execute",
#                "stages":[{"stage":"capability","allow":true,"reason":"白名单放行: jmsx_exec"}]}],
#    "count":1,"total":1,"truncated":false}
```

- `refused=true` 就是「**被拦下的那些**」（演练不算 —— 演练是调用方要的，没拦它）。
- `failed=true` 就是「**活干砸了的那些**」（工具答了错）。
  ⚠️ **门禁拒掉的不算失败** —— 那是门禁在干活，两种情况下都**什么都没跑**；
  而工具失败的那种，命令**可能已经送到机器上了**。这两个问题必须分开问。
- `delivery=maybe_sent` 就是「**可能还在机器上跑着的那些**」——
  失败之后最要紧的一问是「它还在那台机器上跑着吗」，答不出来就只能重试，
  而重试一条可能还在跑的命令，就是把 `rm -rf` 跑第二遍的来路。
  `not_sent` 则是「根本没发出去」，重试是安全的。缺这一格 = 没人说。
  ⚠️ 多机的那两个工具（`jmsx_exec_multi` / `jmsx_collect_multi`）这一格是
  **调用级**的：**任一**主机的命令可能还在跑就是 `maybe_sent`；**逐台的**那一格在
  **答复的每一行**里（每行自己的 `delivery`）——「哪台可能还在跑」看行，不看文字。
  调用级的 `not_sent` 只在**一台都没跑成**、且所有失败都明确说「没送到」时才有
  （那时整批重试是安全的）。
- `client` 是**哪个 MCP 客户端**调的（客户端自报的名字，当线索用可以，当证据用不行）。
- `stages[]` 是**门禁**的判定（门禁放行了，命令是什么只有工具知道）；`refused` 是**工具**的判定；
  `error` 是**这次调用为什么以错误收场**（门禁拒的、工具失败的，都在这一列 —— 要分清就加 `failed`）。
- ⚠️ `allowed=true` 的读法是「**没被拒绝**」，**不是**「跑成功了」—— 失败看 `failed` 那一列。
- `count` 是返回了几行，`total` 是匹配了几行 —— 两者不等就说明被截断了。
- `args` 是当时传了什么（已脱敏，载荷只记长度）。
- 留痕记的是**在这里尝试过什么**，堡垒机记的是**在资产上跑过什么** —— 两者不重叠。

---

## 9. koko TUI 协议（兜底路径，当前实测）

⚠️ **koko 的界面文案变过一轮**，照旧记录写会全盘失败。
完整协议见
[`references/koko-tui.md`](references/koko-tui.md)。

**这条路径可能先于令牌被试**：服务端会记住「上次哪条路走通了」（默认记 10 分钟），
所以令牌机制不可用的部署上，第一次调用之后就直接走终端。
⚠️ 答复里的 `via` 写的是**实际走的那条路** —— 看到 `via` 里没有 "connection token"
就说明这条命令是走终端跑的（此时输出受 40 行窗格限制，要 `full=true` 再拉一次）。

三个必须知道的点：

1. **搜索框要按 `/` 打开**（旧版 `Ctrl+U` 只是顺带聚焦），打完字**还要再按一次 Enter**
   才执行搜索。
2. **搜不到时列表不会变空**，再按 Enter 会选中第一行 ⇒ 可能连到**错误机器**。
   所以搜索生效（提示栏出现 `x:清除搜索`）之后才确认选择。
3. **面板画好 ≠ 能收输入**，第一个 `/` 可能被吞 ⇒ 要带延迟重发。

---

## 10. 约束

- 命令**以资产账号身份执行，不提权**。要 root 自己写 `sudo -n`。
- **一次调用只开一个会话**，调用结束就关闭 —— 不会在堡垒机上留悬挂 shell。
- 资产引用要**精确匹配**（`node` 匹配不到 `node-071`）。
  koko 的搜索按**名称**匹配，所以优先传名称。
- 终端窗口约 40 行，长输出会截断（这是终端路径特有的限制，令牌路径没有）。

> 部署提示：经 VPN/组网链路（tailscale 等）连堡垒机时，会话开可能偶发 SSH 握手 i/o timeout——客户端已内置 15s 握手界与一次重拨（`JMS_KOKO_HANDSHAKE_TIMEOUT` / `JMS_KOKO_DIAL_RETRIES` 可调），偶发失败重试即可。
