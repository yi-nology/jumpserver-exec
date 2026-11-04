# jumpserver-exec — JumpServer 堡垒机的**执行面**（MCP + HTTP API）

让 AI agent **经堡垒机**在受管机器上跑命令、传文件 —— 每一次动手都有录像、有留痕。

同一个工具目录、两个面：**MCP**（stdio / streamable HTTP）与 **REST**
（`/api/v1/…`）—— 同一道门禁、同一套确认语义、同一份留痕。

> 📖 **同时支持 JumpServer v4 与 v5。** 协议层是同一套（端点表、认证头、连接令牌体、响应形状
> 逐条对着上游 v4.10.20-lts 源码核过）。v4 没有 Chat AI 委派方案 ⇒ v4 上的会话只能以 API
> 凭据的身份跑（= 管理员面），`--doctor` 会说明白。

| 仓库 | 是什么 | 工具数 |
|---|---|---|
| [`yi-nology/jumpserver-mgmt`](https://github.com/yi-nology/jumpserver-mgmt) | **管理面**：资产 / 账号 / 授权 / 连通性 / 命令过滤 / 标签 / 审计 / 登录策略 | 97 |
| **`yi-nology/jumpserver-exec`**（本仓） | **执行面**：经堡垒机 koko(2222) 在受管机器上跑命令、传文件 | 15 |
| [`yi-nology/jumpserver-kit`](https://github.com/yi-nology/jumpserver-kit) | 共用底座：协议层 / 门禁 / 输出契约 / 留痕 / 脱敏（**是库，不是服务**） | — |

**AI 访问机器一律经堡垒机**，不接受直连 SSH —— 直连会丢审计录像。

---

## 1. 前置条件

| | 需要什么 |
|---|---|
| 执行面 | 一个 JumpServer 部署（v4 或 v5）+ 一个 **PrivateToken** + 一个堡垒机账号（koko 用来登录的那个）+ 它的 **ssh-rsa 私钥** |

⚠️ **koko 只接受 `ssh-rsa`**：ed25519 会被静默拒绝，而拒绝的样子长得像权限问题。

```bash
ssh-keygen -t rsa -b 4096 -f ~/.ssh/id_rsa
```

生成后把 `~/.ssh/id_rsa.pub` 的内容注册到堡垒机里那个用户的「SSH 公钥」上。

PrivateToken 在堡垒机上拿：v5 是 `设置 → API 授权`，v4 是 `用户 → API 密钥`。

---

## 2. 五分钟装好

### 2.1 安装

```bash
# 方式一：go install（需要 Go 1.26+）
go install github.com/yi-nology/jumpserver-exec@latest

# 方式二：release 二进制（见 Releases 页）
curl -LO https://github.com/yi-nology/jumpserver-exec/releases/latest/download/jumpserver-exec-linux-amd64
chmod +x jumpserver-exec-linux-amd64
```

### 2.2 接线（MCP 配置）

```json
{
  "mcpServers": {
    "jumpserver-exec": {
      "command": "jumpserver-exec",
      "env": {
        "JUMPSERVER_URL": "https://jumpserver.example.com",
        "JUMPSERVER_TOKEN": "<你的 PrivateToken>",
        "JMS_BASTION_HOST": "jumpserver.example.com",
        "JMS_KOKO_USER": "ai",
        "JMS_KOKO_KEY": "~/.ssh/id_rsa"
      }
    }
  }
}
```

⚠️ `JUMPSERVER_URL` 填部署的根地址，**不要带 `/api/v1`**（客户端自己拼），也不带结尾斜杠。
⚠️ `JMS_BASTION_HOST` 填堡垒机主机名（koko 的 2222 端口）。
⚠️ 私钥的 passphrase 别放进 env —— 走 `ssh-agent`，或给一个只有你能读的密钥文件。

### 2.3 接线（HTTP / RPC 面，可选）

同一个工具目录有第二个和第三个面：**REST + MCP over streamable HTTP**，以及
**kitex RPC**（thrift；给其他服务同步调用工具用）。默认都关着，各配两个环境
变量就开：

```json
"env": {
  "JMS_HTTP_ADDR": "127.0.0.1:8732",
  "JMS_HTTP_TOKEN": "<openssl rand -hex 32 生成一个>"
}
```

| 路由 | 干什么 |
|---|---|
| `GET /api/v1/health` | 存活探针（**唯一**不要令牌的路由，只报名字/版本/工具数） |
| `GET /api/v1/tools` | 工具目录：名字、风险、确认要求、入参/出参形状 |
| `POST /api/v1/tools/{name}/invoke` | 调一个工具（Bearer 令牌） |
| `/mcp` | MCP over streamable HTTP（Bearer 令牌） |

⚠️ **没有「不开认证」这回事**：`JMS_HTTP_ADDR` 配了却不配 `JMS_HTTP_TOKEN`，启动即拒。
⚠️ 地址写 `127.0.0.1`，别写 `0.0.0.0` —— 这些路由带着部署自己的凭据去打堡垒机，
暴露到网络需要你自己有代理与 TLS 的故事。
⚠️ **调用结果的读法与 stdio 逐字相同**：拒绝、失败、入参不合形状都是 **HTTP 200 +
`is_error`**（请求没错，结果是拒绝）；请求本身坏才是 400，没有这个工具才是 404。
契约文档在 [`swagger/openapi.json`](swagger/openapi.json)（生成物，与工具同源）。

- **只当服务部署时**用 `jumpserver-exec serve http`（只开 HTTP 面，长跑到信号为止）；
- **给其他服务同步调用工具时**用 `serve rpc`（只开 kitex 面）或 `serve all`
  （HTTP+RPC 常驻；两者都没配拒绝启动；不含 stdio）；
- 默认的 `serve` 是 stdio，配了 `JMS_HTTP_ADDR` / `JMS_RPC_ADDR` 会**顺带**开
  对应的面 —— 一个进程、同一道门禁、同一份留痕。

### 2.4 自检

```bash
jumpserver-exec --doctor   # 接线 + 真实 koko 登录验证
```

退出码：`0` = 正常，`1` = 有问题（看 `FAIL` 行），`2` = 用法错误。
`--doctor` 会做一次**真实的 koko 登录**，并报出认证方式（`key_type` 必须是 `ssh-rsa`）。

装完连不上先看 [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) 的故障对照表。

---

## 3. 工具

它回答的是「**在那台机器上做这件事，会发生什么**」。

| 工具 | 用途 |
|---|---|
| `jmsx_exec` | 单条命令 → stdout+stderr+退出码 |
| `jmsx_run` | 同机多命令，**一次会话**跑完（比多次 exec 便宜得多） |
| `jmsx_exec_multi` | 同一条命令跑多台机器，**带回每台的输出**（`jms_job_run` 没有 stdout）。**默认并发 4 台**。`dry_run=true` 先报「几轮、多久」 |
| `jmsx_collect_multi` | 多台机器采**同一组事实**，返回矩阵（行=机器，列=事实） |
| `jmsx_collect` | 一次会话采集多个事实，返回**带标签的 map** |
| `jmsx_bg_start` / `jmsx_bg_check` | 长任务**后台化**：脱离会话跑，轮询状态与退出码 |
| `jmsx_put_file` | 上传文件（base64 走同一条录像会话）；支持 `local_path` / `content` / `content_base64` |
| `jmsx_get_file` | 读文件；支持 `offset`/`length` 在**远端**取段 |
| `jmsx_reachable` | 能不能开 shell（认证+权限+协议都要通） |
| `jmsx_inventory` | 列堡垒机已知资产，查准确资产名 |
| `jmsx_session_cost` | 一次会话的开销，评估该用 exec 还是 run |
| `jmsx_audit_query` | 查本机留痕：谁试图跑什么、被门禁拦没拦 |

⚠️ **工具数看凭据**：`jmsx_inventory`（列资产）要 REST 凭据才注册 ——
配了 `JUMPSERVER_TOKEN` 是 15 个，没配是 14 个（其余 14 个照常能用）。
`jumpserver-exec tools` 列的是**当前**真正注册了的工具。

---

## 4. 安全模型（这是本仓的重点）

给 agent 一个能在生产机器上跑命令的工具，最大的问题不是功能，是「**谁让它做的、它做了什么、
拦没拦住、它还在不在跑**」。这四件事本仓都做了：

| 机制 | 做什么 | 在哪 |
|---|---|---|
| **门禁** | 危险命令（`rm -rf` 等）**先用 shell 解析器把命令解析成 AST**，再按策略表判只读性 —— 不是字符串匹配（解析器在上游 `oagentkit/tools`，策略表在底座 `pkg/risk`）。危险命令要求 `confirm` 才真跑 | `pkg/risk` + `pkg/reg` |
| **留痕** | 每次调用一行：调了什么工具、参数是什么、门禁每一段的判定与理由、放行与否、耗时。**被拦下的调用根本没到堡垒机**（堡垒机没有会话可查），所以只有这里查得到 | `pkg/audit`，落盘 JSONL |
| **脱敏** | 参数、错误、拒绝理由里的凭据按**同一份词表**遮蔽，两个出口（答复 / 留痕）逐字节一致 | `pkg/redact` |
| **只读模式** | `JMS_READ_ONLY=true` ⇒ 一切会改东西的工具直接拒绝 | 管理面与执行面都支持 |
| **HTTP 面** | REST 与 MCP over HTTP **共用同一个注册表** ⇒ 同一道门禁、同一套确认、同一份留痕；除 `/api/v1/health` 外全部要 Bearer 令牌（常量时间比较），没有「不开认证」模式 | `pkg/httpapi`（底座） |
| **RPC 面** | kitex/thrift 第三张脸，同一注册表同一扇门：拒绝是 is_error **答案**、token 错是**传输错误**；无匿名（目录也认证） | `pkg/rpcapi` + `pkg/faces`（底座） |
| **投递状态** | 失败时区分「命令**没送到**机器」与「**可能还在机器上跑着**」—— 后者绝不重试 | `biz/handler` |

> 上面三个 `pkg/*` 现在住在
> [`jumpserver-kit`](https://github.com/yi-nology/jumpserver-kit) —— 它们是两个服务
> 共用的底座，不认任何一个服务。

`skills/jms-exec-ops/` 是给 AI agent 的操作手册：选工具的正确姿势、写操作怎么确认、
证据链怎么查。**装上 skill 的 agent 才知道怎么正确地用这些工具**。

---

## 5. 已知行为（别当成 bug）

- **命令以资产账号身份执行**，JumpServer 不提权；要 root 自己加 `sudo -n`。
- **koko 的终端窗口约 40 行**，输出超了会被截断；`jmsx_exec(full=true)` 可取全量。
- **tmux 会吞掉资产面板里的空格** —— `Exec` 用 `spaceMark` 替换后再过终端。
- **v4 没有委派凭据方案**，会话只能以 API 凭据身份跑（= 管理员面）。
- **`jmsx_inventory` 没有 REST 凭据就不注册** ⇒ 工具数从 15 变 14（其余照常能用）。

---

## 6. 目录布局

```
.
├── main.go / init.go / server.go / workers.go / shutdown.go
│                                ← 入口层（**仓根**的 package main）：serve / doctor / tools / version
├── cmd/                        ← 子命令（入口层 import 的**普通包**，不是二进制）
│   ├── serve/                  ← 跑服务：stdio MCP（默认）；http/rpc/all 三种子命令选面
│   ├── doctor/                 ← 自检：koko 接线
│   └── tools/                  ← 工具目录：名字、风险、确认要求；`tools openapi` 生成 HTTP 契约
├── biz/                        ← 业务
│   ├── handler/                ← 15 个工具的实现
│   ├── model/jmsexec/          ← 输出契约的生成物（thriftgo，产物入库）
│   └── router/                 ← 装配：注册哪些工具
├── internal/                   ← 模块私有
│   ├── config/                 ← 配置（koko 接线 + REST 端点）
│   ├── repository/koko/        ← 数据访问层：koko TUI 协议握手与输出解析 + 文件传输
│   ├── service/policy/         ← 工具目录的风险侧：要不要 confirm
│   ├── arch/                   ← 机械守卫（见下）
│   ├── middleware/             ← （空）HTTP 中间件：HTTP 面的认证与路由在底座 pkg/httpapi
│   └── rpchandler/             ← （空）RPC 服务端：这一面不对外提供 RPC
├── idl/jmsexec/                ← 输出契约的 thrift 定义
├── pkg/                        ← （空）通用库在 kit 仓，不在这里放东西
├── swagger/                    ← HTTP 面的 OpenAPI（生成物：`tools openapi`，守卫逐字节比对）
├── etc/ deploy/ monitoring/ rpcx/ kitex_gen/
│                                ← （空）hz 骨架里的其余层：见各目录里的 README
├── script/                     ← 构建 / 发布脚本
├── skills/jms-exec-ops/        ← 给 AI agent 的操作手册
├── docs/                       ← 配置与排障
└── AGENTS.md                   ← 给 AI 的迭代指南（改代码前先读）
```

目录按 **CloudWeGo hz 的标准骨架**摆（`cmd/` `biz/` `idl/` `internal/` `pkg/` `etc/` `script/`）。

⚠️ **入口层在仓根**：一个仓一个二进制，二进制就是仓根那个 `package main` ⇒
`go install github.com/yi-nology/jumpserver-exec@latest` 装出来的就是它
（**不是** `…/cmd/jumpserver-exec@latest` —— 那个路径没有了）。

⚠️ **`cmd/` 里是子命令，不是二进制**：那些包是入口层 import 的普通包。
而**实现不住在仓根** —— 仓根那个 package 是 `main`，Go **不许 import 一个
main 包** ⇒ 子命令的实现只能在 `cmd/` 下。

⚠️ **空的层不是「忘了做」**：每一层都有自己的 README，写着它为什么空、
什么时候不再是空的。

### 6.1 为什么底座不在本仓

协议层、门禁、结果整形、输出契约、留痕、脱敏、风险分类 —— 这些**两个服务都认**。
放一个服务仓里，它就是那个服务披着共用名字的私有代码；另一个服务迟早也会伸手进去，
两个服务就这么耦上了，而没有人决定过。

⇒ 它们住在 [`jumpserver-kit`](https://github.com/yi-nology/jumpserver-kit)，
本仓 `require` 一个版本。

### 6.2 守卫（`internal/arch/`）

机械守卫住在测试里，**每个提交都跑**，不靠人记得：

- 工具答复必须是**契约类型**，不是手搓的 map（手搓 map 与契约在 JSON 上分不出来）；
- 凭据词表不许把这个仓自己的参数名误判成凭据（误判 = 留痕不再记录那个参数）；
- **本服务不得 import 管理面**（跨服务 import 由编译器 + go.mod 断言一起守着）；
- 每个依赖都被 `go.sum` 钉住、都能从 module cache 恢复；
- `swagger/openapi.json` 与工具目录**逐字节**对得上（`openapi_test.go`；「会红」这件事
  由 `script/mutate-swagger-guard.py` 的 4 条变异机械证明）。

---

## 7. 文档

| 文档 | 内容 |
|---|---|
| [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) | 环境变量、接线、自检、工具速查、故障对照表 |
| [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) | 构建、发布、迭代流程、常见坑 |
| [`skills/jms-exec-ops/`](skills/jms-exec-ops/) | 给 AI agent 的操作手册 |
| [`AGENTS.md`](AGENTS.md) | 给 AI 的迭代指南：架构不变量、铁律、验证要求 |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | 怎么参与：跑测试、加工具、提交约定 |
| [`SECURITY.md`](SECURITY.md) | 漏洞报告与凭据处理纪律 |

底座与管理面的文档在各自的仓里：
[`jumpserver-kit`](https://github.com/yi-nology/jumpserver-kit) ·
[`jumpserver-mgmt`](https://github.com/yi-nology/jumpserver-mgmt)。

---

## 8. 参与

欢迎提 issue 和 PR。动手前请读 [`CONTRIBUTING.md`](CONTRIBUTING.md) 与
[`AGENTS.md`](AGENTS.md) —— 后者记着这个仓的架构不变量与「已经在哪些地方栽过」的清单，
改代码前扫一眼能省你一轮返工。

⚠️ **别把凭据贴进 issue / PR / 日志**：token、私钥、堡垒机地址都是敏感的。
详见 [`SECURITY.md`](SECURITY.md)。

## 9. 许可

[Apache-2.0](LICENSE)。
