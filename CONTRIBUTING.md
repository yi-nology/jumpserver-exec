# 参与贡献（jumpserver-mcp-exec）

感谢你愿意动手。这个仓的规矩比一般项目多几条 —— 不是为了仪式感，是因为**这个仓的前任栽过跟头，
而那些跟头现在都写成了守卫**，绕开守卫的 PR 会在评审时被挡回来。

## 动手之前

1. 读 [`AGENTS.md`](AGENTS.md) —— 它记着架构不变量（底座不得依赖服务、
   两个服务不得互相 import、契约类型必须真被产出）与「已经在哪些地方栽过」的清单；
2. 读 [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) —— 怎么构建、怎么验证、常见坑；
3. **共享纪律在底座仓**：<https://github.com/yi-nology/jumpserver-mcp-kit/blob/main/AGENTS.md>
   （铁律、底座怎么发版、跨仓守卫）；
4. 认领一个 issue（或者自己开一个，说清要改什么行为）。

## 环境

Go **1.26+**。不需要真堡垒机就能跑全量测试（打真堡垒机的那部分由环境变量 gate 住）。

```bash
./script/build.sh        # 编译 + 跑测试（产物落在 ./bin/jumpserver-mcp-exec）
./script/build.sh --no-test   # 只编译
go test ./...            # 只跑测试
go test -race ./...      # ⚠️ 池的并发护栏要带 -race
gofmt -l .               # 格式化检查（应无输出）
go vet ./...             # 静态检查
```

⚠️ 本仓 `require github.com/yi-nology/jumpserver-mcp-kit v0.1.1`
—— **底座在 [kit 仓](https://github.com/yi-nology/jumpserver-mcp-kit)**，
不在本仓里。离线机器上用 `GOPROXY=off`（只要 module cache 里已经有依赖）。
⚠️ 本地联调（kit 还没发布到 GitHub 时）用**不入库**的 `go.work`：
`go.work` / `go.work.sum` 已在 `.gitignore` 里。

## 改代码的规矩

| 规矩 | 为什么 |
|---|---|
| **用 `reg.Add` 注册工具**，不要用 `mcp.AddTool` | `pkg/reg`（在 kit 仓）是唯一的注册入口，也是门禁所在；绕过它就绕过了「写操作必须 confirm」 |
| **声明输出形状**（`OutputSchema`） | 服务端在返回前按它校验，形状不对直接报错 |
| **答案必须是契约类型**，不许手拼 map | 有机械守卫盯着（`internal/arch/answer_test.go`） |
| **分得清「没投递」与「可能已投递」** | 分不清就是把一条可能还在跑的命令跑第二遍（见 AGENTS.md §1 第 1 条） |
| **加工具要同步改 `skills/`** | 那里是 agent 选工具的依据，有检查会报脱节 |
| **加工具要改工具数** | **15**（没有 REST 凭据时是 **14**：`jmsx_inventory` 只在有 API client 时注册） |
| **改了 IDL 要重新生成** | `./script/gen-idl.sh`，生成物入库 |
| **别跑 `go mod tidy`** | 受限网络下会失败；手工同步 `go.mod` 的 `require` |
| **别提交构建产物** | 产物落在 `./bin/`（发布后落在 `~/.local/jumpserver-mcp-exec-runtime/<stamp>/`） |
| **别在 `go.mod` 里写 `replace`** | 那会让 `go.mod` 与机器绑定；本地联调用不入库的 `go.work` |

## 验证要求（交付前必须过）

- `./script/build.sh` 全绿（编译 + 全量测试）；
- **必须带 `-race` 跑一遍**（连接池的并发护栏在 `internal/exec/koko/pool_test.go`）；
- **工具数、分层、契约三道机械守卫全绿**；
- **改了输出形状要真调一次**（读工具对真堡垒机，写工具对假堡垒机）——
  schema 校验只在答案被返回时才跑，「编译过 + 单测绿」**不等于**形状对；
- 改动 ≥ 300 行，或动了**协议 / 并发 / 权限身份 / 失败路径 / 存量行为** ⇒
  起一个**没参与过的独立审阅代理**看一遍。提示词模板在
  [kit 仓的 `script/review-prompt.md`](https://github.com/yi-nology/jumpserver-mcp-kit/blob/main/script/review-prompt.md)。

## 提交信息

用中文，说明**改了什么行为**，不是「更新代码」。

```
fix(exec): 失败时区分「命令没送到」与「可能还在机器上跑着」
```

好标题：`feat(exec):` `fix(exec):` `chore(script):` `docs:`
—— 行为变了用 `feat`/`fix`，只动文档或脚本用 `docs`/`chore`。

## 别做的事

- ❌ 把凭据贴进 issue / PR / 日志（token、私钥、内网地址）—— 见 [`SECURITY.md`](SECURITY.md)；
- ❌ 顺手改不属于这次改动的代码（review 的人会问「这行为为什么变了」）；
- ❌ import 管理面（另一个服务）；
- ❌ 在共享底座（kit 仓）里伸手进某个服务；
- ❌ **重试一条 `maybe_sent` 的命令**；
- ❌ 用 `git commit -a` 把别人的在制品一起卷进来 —— **只 add 你自己动过的文件**。

## 报告安全问题

**别开公开 issue**，见 [`SECURITY.md`](SECURITY.md)。
