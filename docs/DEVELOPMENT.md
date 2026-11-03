# 开发、构建与发布

> 面向想改这个仓的人。**先读 [`../AGENTS.md`](../AGENTS.md)** —— 那里记着架构不变量与铁律。
> **共享纪律（铁律、底座发版节奏、跨仓守卫）在
> [kit 仓的 AGENTS.md](https://github.com/yi-nology/jumpserver-mcp-kit/blob/main/AGENTS.md)。**

## 1. 环境要求

- Go **1.26+**
- `./script/build.sh` 会跑 `go test ./...`，**不需要**真堡垒机
  （打真堡垒机的那部分由环境变量 gate 住，不设就 skip）
- ⚠️ 本仓 `require github.com/yi-nology/jumpserver-mcp-kit v0.1.1` ——
  **底座在 [kit 仓](https://github.com/yi-nology/jumpserver-mcp-kit)**。
  离线机器上用 `GOPROXY=off`（只要 module cache 里已经有依赖）。
  ⚠️ 本地联调（kit 还没发布到 GitHub 时）用**不入库**的 `go.work`：
  `go.work` / `go.work.sum` 已在 `.gitignore` 里。

## 2. 常用命令

```bash
./script/build.sh                    # 编译 + 跑测试（产物落在 ./bin/jumpserver-mcp-exec）
./script/build.sh --no-test          # 只编译
go test ./...                        # 只跑测试
go test -race ./...                  # ⚠️ 连接池的并发护栏要带 -race
gofmt -l .                           # 格式化检查（应无输出）
go vet ./...                         # 静态检查
./script/gen-idl.sh                  # 从 idl/ 重新生成契约模型（改了 thrift 才需要）
./script/publish-mcp.py              # 发布到运行时目录（打印 mcp update 命令）
./script/bootstrap.sh                # 起一个 stdio 进程，手工看它起不起来
./script/verify-mcp-launch.py        # 启动校验（真的起一个进程打一次）
./script/restore-go-modcache.sh      # 把 Go 的 module cache 补回来
```

⚠️ **`make build` / `make test` 不存在** —— 本仓没有 `Makefile`。

## 3. 源码 → 生效

⚠️ **改源码 ≠ 生效。** 完整链路：

```
改代码 → ./script/build.sh → python3 script/publish-mcp.py → mcp update → 重启会话
```

```bash
python3 script/publish-mcp.py
```

它会打印新运行时目录的路径，以及把 MCP 配置指过去的 `mcp update` 命令。
⚠️ **`publish-mcp.py` 不会改 MCP 配置** —— 配置还指着旧的运行时目录，
于是「真调」调的是**旧二进制**，而你会以为是新代码坏了。
`mcp get <server>` 看 `command` 字段，确认它指向刚发布的那一版，再真调。

### 四个位置的区别（别搞混）

| 位置 | 是什么 | 谁改它 |
|---|---|---|
| 仓库源码目录 | **源码**，唯一真相 | 你 / AI 迭代 |
| `./bin/` | **构建产物** | `script/build.sh` |
| `~/.local/jumpserver-mcp-exec-runtime/<stamp>/` | **运行时二进制**，MCP 配置指向这里 | `script/publish-mcp.py` |
| MCP 配置文件 | **接线**，记录上面那个路径 + 环境变量 | `mcp update` |

## 4. 改一个工具的完整流程

见 [`../AGENTS.md`](../AGENTS.md) §3（底座那一份写着「在服务仓里怎么加」）。要点：

1. **先想清楚代码落在哪一层** —— 工具本身 → `biz/exec/handler/`；
   koko TUI 协议 / 会话 / 池 / 传输 → `internal/exec/koko/`；
   资产清单与 REST → **kit 仓的 `pkg/jmsapi/`**（**别在别处再写一份**）；
2. **用 `reg.Add` 注册**，不要用 `mcp.AddTool`（后者绕过门禁）；
3. **声明输出形状**（`OutputSchema`）—— 服务端返回前会按它校验，形状不对直接报错；
4. **答案必须是契约类型**，不许现拼 map；
5. **在 `biz/exec/policy/policy.go` 里分类**（只读 / 写 / 不可逆 / 条件确认）；
6. **工具描述要写「什么时候用这个工具」**，不是「这个工具做什么」——
   那是 AI 选工具的唯一依据；
7. **同步改 `skills/`** —— 那里是 agent 选工具的依据；
8. **改工具数**（15 → 16）—— 被 `biz/exec/policy/policy_test.go` 钉住
   （⚠️ 没有 REST 凭据时那个数是 14）。

⚠️ **动了会话层就要想清楚投递状态**：改「命令怎么发出去」或「失败怎么分类」时，
必须重看 [`../AGENTS.md`](../AGENTS.md) §1 第 1 条 ——
**分不清「没投递」与「可能已投递」的后果是同一条命令跑两遍**。

## 5. 验证要求（交付前必须过）

| 检查 | 怎么过 |
|---|---|
| 编译 | `./script/build.sh` 无错 |
| 单元测试 | 同脚本内置 `go test ./...`，必须全绿 |
| 竞态 | **必须带 `-race` 跑一遍**（连接池的并发护栏在 `internal/exec/koko/pool_test.go`） |
| 格式化 | `gofmt -l .` 无输出 |
| 静态检查 | `go vet ./...` 无错 |
| 启动 | `./script/bootstrap.sh` 能起进程；`script/verify-mcp-launch.py` 报 `OK ... N tools` |
| 工具数 | **15**（无 REST 凭据时 **14**）—— `biz/exec/policy/policy_test.go` 钉住这个**集合** |
| 目录一致 | 策略表与注册表必须一一对应（同上） |
| 契约 | 契约类型必须真被产出（`biz/exec/model/jmsexec/contract_test.go`）；答案本身必须是契约类型 |
| 分层 | 底座不得依赖任何服务；不得 import 管理面；IDL/包/模型三名对齐（`internal/arch/`） |
| 自检 | `./bin/jumpserver-mcp-exec --doctor` 退出码 0 |
| 真调 | 改了输出形状一定要**真调一次**（读工具对真堡垒机，写工具对假堡垒机）—— schema 校验只在答案被返回时才跑 |

⚠️ **别把「编译过」说成「已完成」。** 汇报时给出：编译结果、测试结果、工具数变化、
发布路径、自检结果，以及「**需要重启会话才生效**」这句话。

⚠️ **改动 ≥ 300 行，或动了协议 / 并发 / 权限身份 / 失败路径 / 存量行为** ⇒ 必须起一个
**没参与过的独立审阅代理**看一遍。提示词模板在
[kit 仓的 `script/review-prompt.md`](https://github.com/yi-nology/jumpserver-mcp-kit/blob/main/script/review-prompt.md)，
里面还有一份「已经在哪些地方栽过」的清单 —— 审的人重点看那里。

## 6. 验证脚本

| 脚本 | 干什么 |
|---|---|
| `script/build.sh` | 编译 + 全量测试 |
| `script/publish-mcp.py` | 发布到运行时目录 |
| `script/verify-mcp-launch.py` | 启动校验（真的起一个进程打一次） |
| `script/bootstrap.sh` | 手工起一个 stdio 进程 |
| `script/gen-idl.sh` | 从 `idl/` 重新生成契约模型 |
| `script/restore-go-modcache.sh` | 把 Go 的 module cache 补回来 |

⚠️ **对着真堡垒机的扫面脚本不在本仓** —— 它们是**某个部署的运维记录**，
跟着那台机器的凭据走，不适合进开源仓。

⚠️ **变异脚本要带三个仓**（在 kit 仓）：
`script/mutate-audit.py`、`script/mutate-version-guard.py` ——
它们变异的代码横跨三个仓 ⇒ 需要**多仓工作集**
（`JMS_SIBLING_ROOTS` 或同级目录约定）。

## 7. 常见坑

### 7.1 沙箱按路径执行策略

在**受执行策略限制的环境**里，往一个**已写过**的路径覆盖二进制，它就不再能启动：
exit 137、无 stderr。

⇒ 所以发布一律进**新的时间戳目录**（`publish-mcp.py` 已经这么做了），别自己写 `cp` 覆盖。

### 7.2 Go module cache 被清空

缺模块时报 `replacement directory ... does not exist`。
修法：`./script/restore-go-modcache.sh`。⚠️ **不要**去改 `go.mod` 的 `replace` 路径。

### 7.3 别跑 `go mod tidy`

它要抓测试专用依赖（x/tools、x/term），在受限网络下必然失败。
改依赖版本就手工同步 `go.mod` 的 `require`（本仓不写 `replace`）。

### 7.4 代理

本机 `proxy.golang.org` 不可达时，`goproxy.cn` 可用。构建用 `GOPROXY=off`（脚本已设）。

### 7.5 源码树里不要留构建产物

产物落在 `./bin/`。历史上编辑器备份文件、swap 文件、零字节标记
（`*-go-tmp-umask`）把源目录搞得像垃圾场 —— 已清理，别再产生。

### 7.6 底座改了编译不过

本仓的 `require` 指向 kit 的某个版本，而那个版本还没发。
⇒ **先发 kit 的 tag**，再更新本仓的 `require`（见 kit 仓的 CONTRIBUTING）。

### 7.7 每次会话都慢十倍

委派在 v4 上回 401，而 401 在那条路上被读成「令牌机制不可用」⇒
**每次会话都被踢去慢十倍的 koko 终端路径**。
⇒ 能力档（kit 的 `pkg/jmsapi/capability.go`）在 v4 上**根本不发起委派**；
用 `--doctor` 的 `version` 一行确认部署是几版。

## 8. 提交约定

- 提交信息用中文，说明**改了什么行为**（不是「更新代码」）；
- 提交前确认 `git status` 里**没有**构建产物、备份文件、二进制（`./bin/` 不入库）；
- `go.work` / `go.work.sum` **不入库**（见 `.gitignore`）；
- 别把运行时配置里的 token 提交进来 —— 那是仓外的配置。
