# 借鉴 kael 设计改进 jumpserver-exec — 设计文档

日期：2026-11-16
参考：https://github.com/jumpserver/kael （JumpServer AI 组件，Panel/SSE 执行生命周期）
方向：**借鉴设计改进现有程序**，不接入 kael 通道，不向 kael 注册工具。

## 背景与现状

kael 的执行生命周期有四个机制值得借鉴。对照 jumpserver-exec（执行面 MCP，
直连 koko 2222，18 工具）现状：

| kael 机制 | 我们现状 | 差距 |
|---|---|---|
| 审批记忆（Approval 绑定 Panel+Registration+参数摘要，10 分钟可复用） | `confirm=true` 每次调用一次性 | 重复跑同一条危险命令时每条都要确认 |
| 回执超时 ⇒ `execution_state_unknown`，保留部分输出，绝不重放 | 超时判 `maybe_sent`、文案已含 "may still be running"，但**部分输出被丢弃**，无显式状态词 | 超时错误不带部分输出；无 kael 词汇 |
| 显式取消（`POST /runs/{id}/cancel`，断开≠取消） | bg 命令用 `setsid nohup` + `.rc` 标记，**没记 pid，取消不掉** | 缺取消工具与取消语义 |
| 终态五态 running/success/error/cancelled/timeout | bg_check 只有 running/finished 两态 | 状态词汇未对齐 |

红线（不变）：超时 = `maybe_sent`，**绝不重放**（kit 铁律 1）；已投递绝不重放
管着连接池；`put_file`/`put_dir`/`relay` 无条件确认格局不变。

## A. 审批记忆

- 落点：`internal/service/policy/memory.go`（policy 层是工具目录风险侧的家）。
- 数据结构：进程内 `map[key]time.Time`（存过期时刻），`sync.Mutex` 保护。
- 键：`(asset, sha256(strings.TrimSpace(command)))`。只存哈希不存原文（命令文本可能含敏感参数）。
- TTL：默认 **10 分钟**，环境变量 `JMS_APPROVAL_MEMORY_TTL`，`0` = 关闭记忆。
- 授予：`confirm=true` 通过门禁的那一刻记下键。
- 命中：DANGEROUS 且无 `confirm` 时查记忆；命中且未过期 ⇒ 视为已确认，
  留痕写明 `confirmed via remembered approval granted at <时间>`（审计可见，非静默）。
- 范围：仅条件确认工具——`jmsx_exec` / `jmsx_run` / `jmsx_exec_multi` /
  `jmsx_collect` / `jmsx_collect_multi` / `jmsx_bg_start`。
  `put_file` / `put_dir` / `relay` / `bg_check(cleanup)` 不纳入。
- `dry_run` 照常反映记忆（命中时 `would_execute=true`）。
- 只读模式（`JMS_READ_ONLY=1`）不受影响：记忆只放宽确认，不放宽只读拒写。

## B. 超时语义对齐

- koko 层（`internal/repository/koko`）：命令跑过 timeout 的错误**携带截断的部分输出**
  （有界，封顶 4KB，取输出尾部）。新增带输出的超时错误类型，实现 `Unwrap` 保持
  `errors.Is` 链不断（现有 `maybe_sent` 分类与守卫测试不受影响）。
- 错误文本以 `execution_state_unknown: the command may still be running on the machine`
  开头，随后附部分输出。词汇与 kael 对齐，行为红线不变。
- sweep/multi 各台的 `delivery` 格通过既有 `errFields` 自动继承。
- 回归测试：超时错误必须同时含状态词与部分输出（缺一则红）。

## C. 后台命令取消（新工具 `jmsx_bg_cancel`）

- bg_start wrapper 增加第三个标记文件：wrapper 的 pid 写入 `<path>.pid`。
  setsid 后该 pid 即进程组组长，`kill -TERM -<pgid>` 可整组终止（内层 shell 一起）。
  答复（BgStartResult）新增 pid 标记路径与 `jmsx_bg_cancel` 提示。
- `jmsx_bg_cancel(asset, path, confirm)`：
  - 无条件 `confirm=true`（取消 = 对机器的写，同 put_file 档）。
  - 流程：读 pid 文件 → `kill -TERM -<pgid>` → 轮询最多 5s 确认退出
    → 写 `.rc=143` 与 `.cancelled` 标记（含时间戳）。
  - 任务已结束 ⇒ 如实报 `already finished rc=<code>, not cancelled`，不覆盖 `.rc`。
  - pid 文件缺失 ⇒ 报 `cancel unsupported: pid marker missing`（老任务没有 pid 文件）。
  - 取消本身是机器上的新命令，走标准投递/失败分类；取消结果有确认回执（终态）。
- 工具数 **18 → 19**（无 REST 凭据时 **17 → 18**）。

## D. 终态五态对齐

- `bg_check` 状态词汇对齐 kael：`running / success / error / cancelled / timeout`。
- 映射：`finished`+rc=0 ⇒ `success`；`finished`+rc≠0 ⇒ `error`；
  存在 `.cancelled` 标记 ⇒ `cancelled`（优先于 `.rc` 判定）；
  仍在跑且超过 TTL ⇒ `timeout`。
- `finished` 不再作为对外状态值。
- TTL：`bg_start` 写 `.started` 时间戳；`JMS_BG_TTL`（默认 `0` = 不启用），
  超时仍在跑时 check 报 `timeout` + 警告。默认关闭，避免惊吓式状态。

## 契约（IDL）改动

1. `BgCheckResult.state`：状态词表更新（注释写明五态）。
2. `BgStartResult`：新增 `pid_marker` 可选字段。
3. 新增 `BgCancelResult`（asset/path/state/note/confirm 相关字段）。

生成物入库（thriftgo slim 模板产物），`./script/gen-openapi.sh` 重生 swagger
（守卫逐字节比对）。policy 集合测试同步（want+1 逻辑天然兼容，但名字集合要加新工具）。

## 同步改动清单

- `internal/service/policy/`：memory.go + policy 表加 `jmsx_bg_cancel`（写/无条件确认）。
- `biz/handler/bg.go`：pid 标记、cancel 工具、五态映射、`.started` 时间戳。
- `biz/handler/dryrun.go` 及确认门各调用点：接记忆。
- `internal/repository/koko/`：超时错误带部分输出。
- `internal/config`：`JMS_APPROVAL_MEMORY_TTL` / `JMS_BG_TTL`。
- `idl/` + `biz/model/jmsexec/` + `swagger/`：契约与生成物。
- `internal/service/policy/policy_test.go`、contract/arch 守卫照常。
- 文档：`README.md` / `docs/CONFIGURATION.md` / `skills/` / `AGENTS.md` 工具表、
  `CHANGELOG.md`。

## 测试要求

- memory：授予/命中/过期/TTL=0 关闭/键不含命令原文/多工具共用。
- 超时：错误含状态词 + 部分输出 + `errors.Is` 链不断。
- cancel：命令构造与解析（builder/parser 成对测）、已结束不覆盖、缺 pid 文件的拒绝。
- 五态：各态映射单测；`.cancelled` 优先于 `.rc`。
- 全量 `./script/build.sh`（含 `-race`）+ `--doctor` + 真调（含新工具真调一次）。

## 风险与红线复核

- 动了失败路径（B）与会话层 ⇒ 铁律 8：起独立 verifier 子代理（提示词用
  kit 仓 `script/review-prompt.md` 模板），审阅结论贴进汇报。
- 工具数变化口径：对外 19（无凭据 18），两个数字不混。
- A 是安全语义放宽：必须留痕可见（granted + remembered 都记），
  只读模式不被绕过；TTL 默认 10m 可调可关。
