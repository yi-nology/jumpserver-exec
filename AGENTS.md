# AGENTS.md — jumpserver-exec 迭代指南

> 给 AI agent 的作业说明。**改这个仓的代码前先读完本页**，尤其第 1、2 节。

本仓 = JumpServer 堡垒机的**执行面** MCP 服务：经堡垒机 koko(2222) 在**受管机器**上
跑命令、传文件。**16 个工具**，一个二进制 `jumpserver-exec`。
面向人读的完整说明在 [`README.md`](README.md)，
**配置与排障看 [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md)**。

---

## 0. 先认位置：本仓只是三个仓里的一个

| 仓 | 是什么 | 干什么 |
|---|---|---|
| [`yi-nology/jumpserver-mgmt`](https://github.com/yi-nology/jumpserver-mgmt) | MCP server `jumpserver` | **管理面** — 改堡垒机自己的配置 |
| [`yi-nology/jumpserver-exec`](https://github.com/yi-nology/jumpserver-exec)（本仓） | MCP server `jumpserver-exec` | **执行面** — 经堡垒机在**受管机器**上跑命令、传文件 |
| [`yi-nology/jumpserver-kit`](https://github.com/yi-nology/jumpserver-kit) | 共用底座（**库**） | 协议层 / 门禁 / 输出契约 / 留痕 / 脱敏 / 风险分类 / 共用词汇 / 守卫的机械 |

**共享纪律（铁律、验证要求、提交约定、底座怎么发版）在 kit 仓的 AGENTS.md 里**：
👉 <https://github.com/yi-nology/jumpserver-kit/blob/main/AGENTS.md>

⚠️ **只读本仓的 agent 也要拿到底线** ⇒ §1 把最关键的铁律**原文重复**了一遍。
⚠️ **底座在 kit 仓**：本仓 `require github.com/yi-nology/jumpserver-kit`，
协议层（`pkg/jmsapi`）、门禁（`pkg/reg`）、结果整形与输出契约（`pkg/render`）、
留痕（`pkg/audit`）、脱敏（`pkg/redact`）、风险分类（`pkg/risk`）、
失败分类（`pkg/errs`）、共用词汇（`model/jms`）**都不在本仓**。

**目录约定**（CloudWeGo hz 的标准骨架）：

```
main.go / init.go / server.go / workers.go / shutdown.go
                             # 入口层（**仓根**的 package main）：serve / doctor / tools / version
cmd/                        # 子命令（入口层 import 的**普通包**，不是二进制）
├── serve/                  #   跑服务：stdio MCP（默认）；`serve http` / `serve rpc` / `serve all` 选面
├── doctor/                 #   自检：koko 接线
└── tools/                  #   工具目录：名字、风险、确认要求；`tools openapi` 生成 HTTP 契约
biz/
├── handler/                # 16 个工具的实现
├── model/jmsexec/          # 契约的生成物（thriftgo slim 模板，产物入库）
└── router/                 # 装配：注册哪些工具
internal/
├── config/                 # 配置（环境变量 → 结构体）
├── repository/koko/        # 数据访问层：koko TUI 协议 / 连接令牌 / 连接池 / 文件传输
├── service/policy/         # 工具目录的**风险侧**：每个工具改不改机器、要不要确认
│   │                       # （RPC 面与 REST/stdio 同用这一份 policy 表与注册表）
├── arch/                   # 机械守卫（判据；守卫体在 kit 的 pkg/arch）
├── middleware/             # （空）HTTP 中间件：HTTP 面的认证与路由在底座 pkg/httpapi
└── rpchandler/             # （空）RPC 服务端：这一面不对外提供 RPC
idl/                        # 契约的 thrift 定义（摊平布局）：入口 jmsexec.thrift + 按域拆的多份
pkg/                        # （空）通用库在 kit 仓，不在这里放东西
swagger/                    # HTTP 面的 OpenAPI（生成物：tools openapi，守卫逐字节比对）
etc/ deploy/ monitoring/ rpcx/ kitex_gen/
                             # （空）hz 骨架里的其余层：见各目录里的 README
script/                     # 构建 / 发布 / 生成 / 校验
skills/                     # 给 AI 的操作手册
```

⚠️ **入口层在仓根**：一个仓一个二进制，二进制就是仓根那个 `package main` ⇒
`go install github.com/yi-nology/jumpserver-exec@latest` 装出来的就是它
（**不是** `…/cmd/jumpserver-exec@latest` —— 那个路径没有了）。

⚠️ **`cmd/` 里是子命令，不是二进制**：那些包是入口层 import 的普通包。
而**实现不住在仓根** —— 仓根那个 package 是 `main`，Go **不许 import 一个
main 包** ⇒ 子命令的实现只能在 `cmd/` 下。

⚠️ **空的层不是「忘了做」**：每一层都有自己的 README，写着它为什么空、
什么时候不再是空的。

hz 的 `router.go` / `router_gen.go` 在这里没有对应物：HTTP 面的路由与认证
住在底座的 `pkg/httpapi`（两个面共用），`biz/router` 是**工具目录的装配**——
它们的对应物。

**分层的方向是硬的，有守卫盯着**（判据在本仓 `internal/arch/`，守卫体在 kit 的 `pkg/arch`）：
- **本仓不得 import 管理面**（另一个服务）；**底座不得依赖任何服务**。
- **`cmd/` 是最外层，谁都不许 import**。
- **IDL 目录 ↔ 生成包名 ↔ 模型目录名一一对应**：本仓单词汇摊平在 `idl/`，
  包名由全目录一致的 `namespace go jmsexec` 命名，生成的包必须叫 `jmsexec`、
  放在叫 `jmsexec` 的目录里（守卫 pkg/arch 认平铺与嵌套两种布局）。

---

## 1. 铁律（原文重复 —— 完整版在 kit 仓的 AGENTS.md）

> 完整版：<https://github.com/yi-nology/jumpserver-kit/blob/main/AGENTS.md> §2
> 下面这几条是**只读本仓也要拿到底线**的。

1. **⚠️ 失败之后，绝不能重试一条 `maybe_sent` 的命令。**
   「它还在那台机器上跑着吗」答不出来就只能重试，而重试一条**可能还在跑**的命令，
   就是把 `rm -rf` 跑第二遍的来路。
   投递状态由**知道它的那一层**带着走（会话层）：`not_sent` = 根本没离开这个进程
   （重试是安全的）；`maybe_sent` = **可能已经在机器上跑了**（**永不重放**）；
   **缺这一格 = 没人说**，不是「肯定没送到」。
   ⇒ 工具层要把带类型的错误**原样交上去**（`return fail(err)`，
   `biz/handler/fail.go`），别用 `render.Failf("%v", err)` 拍平 ——
   拍平 ⇒ 投递状态没了 ⇒ 留痕只会说「它失败了」，不会说「它可能还在机器上跑着」。
   ⚠️ agent 看到的文字**一个字没变**（登记处把错误原样转成错误结果）。
   ⇒ **已投递绝不重放**这条规矩还管着连接池：同一连接一次只跑一条命令；
   只有「通道没开起来」才重试一次。

2. **改源码 ≠ 生效。** 必须走完：`./script/build.sh` → `python3 script/publish-mcp.py`
   → `mcp update` → **重启会话**。只改代码就汇报「已完成」是错的。
   ⚠️ **`publish-mcp.py` 不会改 MCP 配置** —— 配置还指着旧的运行时目录，
   于是「真调」调的是**旧二进制**，而你会以为是新代码坏了。
   `mcp get <server>` 看 `command` 字段，确认它指向刚发布的那一版，再真调。

3. **不要原地覆盖二进制。** 沙箱按路径执行策略，覆盖已存在路径的二进制会被 SIGKILL
   （exit 137、无 stderr）。发布必须进新的时间戳目录 —— `publish-mcp.py` 已经这么做了，
   别自己写 `cp` 覆盖。

4. **构建报 `replacement directory ... does not exist`** ⇒ Go module cache 被清空了。
   跑 `./script/restore-go-modcache.sh`，**不要**去改 go.mod 的 replace 路径。

5. **别跑 `go mod tidy`** —— 它要抓测试专用依赖（x/tools、x/term），沙箱下必失败。
   改依赖版本就手工同步 `go.mod` 里的 `require`。

6. **构建用 `GOPROXY=off`**（脚本已经设了），**且不要带 `-mod=mod`** ——
   只接受 `readonly`/`vendor`，带了会直接报错。

7. **源码树里不要留构建产物。** 产物落在 `./bin/`（发布后落在
   `~/.local/jumpserver-exec-runtime/<stamp>/`）。历史上编辑器备份文件、
   swap 文件、零字节标记（`*-go-tmp-umask`）把源目录搞得像垃圾场，**已清理，别再产生**。

8. **大改之后要有一双没参与过的眼睛。** 改动 ≥ 300 行，或动了**协议 / 并发 /
   权限身份 / 失败路径 / 存量行为**其中任何一项，就必须起一个**独立 verifier 子代理**
   审一遍，提示词用 [`script/review-prompt.md`](https://github.com/yi-nology/jumpserver-kit/blob/main/script/review-prompt.md)
   里的模板。
   - **为什么不是「自己再读一遍」**：自己写的代码自己看不出盲区。
   - **审阅结论要贴进汇报**：修了哪几条、哪几条判定为不属实。**没贴就等于没审。**
   - ⚠️ **审阅起不来时的退路**：子代理审阅会长时间零输出（两轮实测）⇒
     **别干等**，改成**按审阅清单逐条自己核**。

9. **用 `reg.Add` 注册，不要用 `mcp.AddTool`。** `pkg/reg`（在 kit 仓）是**唯一**的
   注册入口，也是门禁所在。用 `mcp.AddTool` 就绕过了门禁，
   而门禁是「写操作必须 confirm」这条规则的唯一执行者。

10. **声明输出形状（`OutputSchema`），而且答案本身必须是契约类型。**
    服务端在返回前按 schema 校验，形状不对直接报错；机械守卫
    （`internal/arch/execution_test.go` 的 `TestTheAnswerTypesAreFoundMechanically`）盯着「不许手拼 map 的答案或行」。
    ⚠️ **契约类型写了没人产出 = 契约不存在**
    （`biz/model/jmsexec/contract_test.go` 盯着）。

11. **底座改动会波及管理面** ⇒ **改底座前先发 kit 的 tag**，再更新两个服务仓的
    `require`。⚠️ **不许在本仓的 `go.mod` 里写 `replace`**（那会让 `go.mod`
    与机器绑定）；本地联调用**不入库**的 `go.work`。

12. **AI 访问机器一律经堡垒机 koko(2222)**，不接受直连 SSH —— 直连会丢审计录像。

13. **别把「编译过」说成「已完成」。** 汇报时给出：编译结果、测试结果、工具数变化、
    发布路径、自检结果、以及「**需要重启会话才生效**」这句话。


## 2. 执行面的实现要点（`biz/`）

### 2.1 开会话有两条路

（`internal/repository/koko/backend.go` 选）
- **连接令牌**（默认）：`POST /api/v1/authentication/connection-token/` 换一个一次性凭据，
  当 SSH 凭据直连 koko 2222（用户名 `JMS-{token id}`，密码是令牌的 value）。
  实测开会话 **0.7~0.9s**、单条命令 0.15~0.25s、**退出码是原生的**、输出不截断。
- **koko TUI**（兜底）：走一遍资产面板，**7.25~9.12s**。
- ⚠️ **只有令牌「机制」不可用时**（端点不存在、部署不提供）才回退 TUI；
  资产本身的问题（没账号、没权限）**不回退** —— TUI 也会以同样理由失败，只是慢十秒。
- ⚠️ **`account` 要账号 UUID**（v4/v5 都是），填账号名/alias 会被拒（`perm_account_invalid`）。
  这就是「koko 拒绝连接令牌」这个错误结论的来源（第三方仓说 v4 要 alias）。
  ⚠️ **那条说法只对动态账号成立**：v4 的 `get_asset_accounts_by_alias` 见到 UUID 就按 id 查，
  且普通账号的 `Account.alias` 本就等于它的 id ⇒ **UUID 在 v4 上照样通**。
- ⚠️ **令牌默认用委派凭据签发**，令牌的 `user` 就是被委派用户（例如 `ai`，即你自己的那个账号），
  权限面与从前一致；没配到委派 secret 就会用 API token 签 ⇒ **那是管理员权限面**。
  `jumpserver-exec --doctor` 的 `identity` 一行会写明当前是哪种。
- ⚠️ **v4 上没有委派**（没有 `chat_ai` 应用）⇒ 令牌只能用 API 凭据签 ⇒ 管理员权限面。
  ⚠️ **这不是配置能解决的**：v4 上不存在更窄的凭据。
  ⚠️ 而且**必须关掉委派**：委派请求不带 API token ⇒ v4 回 401 ⇒ 401 在那条路上被读成
  「令牌机制不可用」⇒ **每次会话都被踢去慢十倍的 TUI 路径**。
  ⇒ 能力档（kit 的 `pkg/jmsapi/capability.go`）在 v4 上**根本不发起委派**；
  `pkg/arch` 的守卫盯着「每个签委派凭据的地方都要先问能力」
  （门卫必须是 `if` 的 Init/Cond 里的**能力提问**，且**支配**调用点）。
  ⚠️ **包限定调用不算提问** —— `jmsapi.CostOf(cap)` 是**答**不是**问**；
  曾经被误当成门禁，于是一个**没门禁**的委派调用蒙混过关（已用变异验证过：
  摘掉这条修复，守卫对那一击**一声不吭**）。
  ⚠️ 门禁的**极性**也要对：拿 `== CapabilityAbsent` 当门禁 = 恰恰在
  **没有**这个能力的部署上把凭据递出去（守卫现在也抓这个）。
  ⚠️ 而且**极性检查要看整个条件、不能只看顶层**：`dc != nil && state == CapabilityAbsent`
  把比较藏在 `&&` 的下一层，只看顶层就漏了（**这是守卫生成的那轮才发现的洞**，
  由 `script/mutate-version-guard.py` 的第 4 条变异逮到）。
  ⚠️ **守卫的牙齿有回归保护**：`script/mutate-version-guard.py`（5 条变异，
  5/5 被杀）＋一条**登记在案的边界**（「反门禁 + 摘掉极性检查 ⇒ 没人抓」，
  因为极性检查是唯一挡它的东西 ⇒ 边界写下来，而不是靠人记）。

### 2.2 后端记忆

（`internal/repository/koko/memory.go`）记住**哪个后端上次通了**（按堡垒机
一条，不是按资产 —— 令牌是每次现签的，koko 拒令牌登录就是拒所有）。
- 它**只改尝试的顺序**（记忆说 TUI 通，就先试 TUI），**从不跳过一条路**，
  也**从不把资产问题变成兜底**。
- ⚠️ **记忆会过期**（`JMS_BACKEND_MEMORY_TTL`，默认 10m）：过期后回到默认顺序
  （先令牌）—— 后端恢复了只能靠**再试一次**找回来，记忆不掩盖恢复。
- ⚠️ **TUI 优先时先做资产检查**（`checkAssetUsable`）：走一遍终端要十秒，
  而没账号的资产终端也一样拒 —— 先查再走。⚠️ 这一查是**实时**的
  （不走 resolver 的 5 分钟缓存）：缓存会说「刚授权的账号不存在」，
  而终端的面板是实时问堡垒机的 —— 拿缓存做**硬拒绝**会把能连的机器拒掉。

### 2.3 自愈重选

（`internal/repository/koko/token.go` 的 `degrade`）连接断了、重连又失败，
而失败是**机制**问题（令牌这条路不通了）⇒ 换 TUI 跑这条命令。
⚠️ **只在「命令没投递」这一支**（`errNotDelivered`）—— 换路执行是**第一次投递**，
不是重放；「已投递绝不重放」那条规矩仍然成立。
答复的 `via` 写**实际走的那条路**（降级后不再说「连接令牌」）。
⚠️ 降级后**账号是未知的**（终端路径由 koko 选账号，我们不选）⇒ 宁可说不知道，
也不报令牌路径解析出来的那个账号 —— 报错了就是在说「它以 opsuser 跑的」，
而它可能以 root 跑的。身份（identity）则是已知的：走终端时堡垒机授权的就是那个用户。

### 2.4 连接是池化的

（`internal/repository/koko/pool.go`）令牌单次，但**会话活得比令牌久**
（实测 7 分钟），所以同一台机器的第二次调用几乎免费（1.04s → 0.26s）。
⚠️ 池里的连接**不出现在堡垒机的会话列表里**（实测：20 台 sweep 后仍是 0 条未结束会话 ——
堡垒机记的是**每条命令的 exec 会话**，跑完就结束）；但资产上会留一个
`sshd-session: <user> [priv]` 进程。`JMS_POOL_TTL` 定的就是这些进程挂多久。
同一连接一次只跑一条命令；**已投递的命令绝不重放**，只有「通道没开起来」才重试一次。
⚠️ **安全读法**：池里的连接带着它被签发时的权限。**权限被撤销后，已经开着的
连接仍然能用** —— 所以 TTL 也是「被撤销的权限多久才真正失效」。

### 2.5 「已投递」和「没投递」必须分得开

SSH 协议给了一个精确的判据：服务器**先回包再执行**（RFC 4254），
而 x/crypto/ssh 里 `mux.SendRequest` 的两种失败是分开的 ——
**写失败**返回写错误（命令没发出去，**可以重试**），
**回包丢了**返回 `io.EOF`（命令可能已经在跑了，**永不重放**）。
⚠️ 这条是本仓最重要的一条：**分不清就会把一条可能还在跑的命令跑第二遍**。

### 2.6 传输与关连接

- **文件传输走 exec 通道的 stdin/stdout**（不是 base64 分块）：实测 1MB 上传 1.1s、
  下载 1.0s（base64 要 25s）。⚠️ 只有连接令牌后端有 —— 终端路径是往屏幕里打字。
- ⚠️ **写文件是原子的**：先 `readlink -f` 解析目标（符号链接要保住链接本身），
  写到**同目录的临时文件**，最后 `mv` 改名 —— 传输中途失败不会毁掉原文件。
- ⚠️ **关连接必须先取连接的锁**：另一个会话可能正在这条连接上跑命令，
  裸 `client.Close()` 会把那条命令打断一半，而**已投递的命令不能重放**。
  （这条是 `pool_test.go` 里的并发测试抓出来的 —— 补丁没生效，测试当场抓住。）
- `internal/repository/koko/transfer.go` 走 base64，传输后有大小校验（截断会显式失败，
  不会留半个文件）。下载支持 `offset`/`length` 在**远端**取段（`dd skip/count`），
  不必整文件拉回。
- **`jmsx_relay`**：一台机器的文件直接传到另一台，字节从源端通道进目标端通道，
  **不落本地磁盘**；两端各算一次 sha256。

### 2.7 koko TUI 协议（兜底路径）

- 核心难点是 **koko TUI 协议**：`internal/repository/koko/koko.go` 负责握手和输出解析（兜底路径）。
- ⚠️ **TUI 的屏幕缓存是原始字节流，不是渲染后的屏幕**：tmux 用转义序列移动光标、
  再把行写在落点，所以「状态栏」和「载荷行」会落在**同一条流**上（中间隔着转义
  序列）。编号行的匹配要求「列以数字开头」⇒ 粘在一起时整行被丢 ⇒ 答复里**只有
  退出码、没有输出**（实测 16 次里 1 次，新会话的首条命令）。
  ⇒ 编号行一定从**某个转义序列之后**开始（`lastNumberedStart`）；
  真实抓到的屏幕固件在 `internal/repository/koko/testdata/tui-glued-row.txt`，
  回归测试在 `glued_row_test.go`。
- ⚠️ **koko 的界面文案会变**，就绪信号、搜索触发方式、否定文案都在变。
  当前流程见 `koko.go` 里的 `panelHint`/`searchOpen`/`searchClear`
  常量注释，以及 skill `jumpserver-ops` 的「koko TUI 协议」表。
  **改握手前先照着当前屏幕抓一遍，别照着旧记录写。**
- ⚠️ **tmux 会吞掉资产面板里的空格** —— `Exec` 用 `spaceMark` 占位后再还原。
  `debug_spaces.go` 记录了当初怎么定位的，**别删**。
- ⚠️ **终端窗口约 40 行，长输出会截断**（**只在 TUI 兜底路径上**；
  令牌路径的输出是完整的）。要完整输出用 `full=true`。
- ⚠️ **超时必须真的有界**：命令超时后要再给传输一点时间收尾，但**不能无限等** ——
  传输卡死时会把连接的锁一直占着，同一台机器上的其他会话全被连累，而且它们
  自己的超时根本轮不到开始。连接的锁因此是可带截止时间等待的（`connLock`）。
- 池的并发护栏在 `internal/repository/koko/pool_test.go`，**构建脚本每次都跑 `-race`**。

### 2.8 成本有三个数，不是两个

（`internal/repository/koko/cost.go`）**冷开**（新建连接）、**池命中**（复用已有连接）、
**一条命令**。sweep 每台机器都是冷开，所以计划用冷开价；
重复调用同一台机器才是池命中价。样本**按后端打标签**（令牌 ~1s vs 终端 ~8s，
混在一起的中位数两边都不属于）。计划**会去查池**并报出 `pooled_hosts` ——
已经连过的主机在重复 sweep 时几乎免费。

### 2.9 执行面没有自己的 REST 客户端

- 资产清单走的是 kit 的 `pkg/jmsapi`，跟管理面同一份实现。
  配置在 `internal/config` 的 `Config.API`（`JUMPSERVER_URL` / `JUMPSERVER_TOKEN` /
  `JUMPSERVER_TIMEOUT` / 限流），**别再在代码里 `os.Getenv` 或硬编码堡垒机地址**。
  没有 token 时 `Config.APIClient()` 返回 nil：`jmsx_inventory` 停用，令牌后端也停用
  （于是走 TUI 兜底）。
- ⚠️ **`account` 必须是账号 UUID**（不是名字/alias）；资产清单在执行面**缓存 5 分钟**：
  多机扫描每台机器都要解析一次资产，没有缓存就会「每台机器拉一次全量资产清单」，
  扫描时间全花在列清单上。

### 2.10 输出形状全部是契约类型

- ⚠️ **执行面 16 个工具的输出形状全部是契约类型**（`idl/`），
  `render.Object` 一个不剩（`biz/handler/contract_test.go` 盯着）。
  契约类型由工具**直接产出**，不是现拼的 map —— 所以键名写错编译期就红。
- 契约词汇：`SessionStats`（会话的账：握手多久、省了多少）、
  `SweepPlan`（一次批量作业的估算：几轮、多久、并发拉满能省多少）、
  `Check`/`Fact`（要采的事实 / 采回来的事实）、`MatrixRow`（批量采集里的一台）、
  `HostResult`（批量执行里的一台）、`BatchResult`/`CollectResult`/`ReachableResult`/
  `PutFileResult`/`GetFileResult`/`BgStartResult`/`BgCheckResult`/`SessionCostResult`。
- ⚠️ `koko.Session.Stats()` 直接返回 `*jmsexec.SessionStats` —— 那六个键是固定的，
  不是动态 map，**三个仓里只有这一份定义**（两处各写一份必然漂移）。
- ⚠️ 同一工具的「跑完了 / 被拦了 / 演练过」共用一个契约类型：演练与被拦时
  **没有结果字段**（什么都没跑，没有结果可言），别再塞一个空的 `results`。

### 2.11 命令与工具的行为约定

- **命令以资产账号身份执行，不提权** —— 要 root 自己写 `sudo -n`。
- 执行面另外提供**只读模式**（`JMS_READ_ONLY=1` 拒掉所有写工具，只留采集与清单）。
- 执行面的命令工具都有 `dry_run=true`（`jmsx_exec` / `jmsx_run` / `jmsx_collect` /
  `jmsx_collect_multi` / `jmsx_exec_multi`）：不执行，只报「这条命令被判成什么、
  会不会执行」。
- **多机的两个工具**（`jmsx_collect_multi` / `jmsx_exec_multi`）的演练答复里还带一个
  `plan`：**几轮**（轮数）、**大概多久**（秒数区间）、并发拉满能省多少。
  底数来自 `internal/repository/koko/cost.go`：**握手与单命令耗时是实测的**（`koko.Open` 计时，
  取中位数），没量过时用文档默认值并标出来。
  ⚠️ 演练**不开会话**，所以 `plan` 是按已测成本推算，不是「跑过了」。



---

## 3. 执行面专属铁律

1. **工具数是 16，**没有 REST 凭据时**是 15**。
   `internal/service/policy/policy_test.go` 断言的是**名字集合**而不是一个数字：
   15 个工具是固定的（`jmsx_exec` / `jmsx_run` / `jmsx_collect` / `jmsx_collect_multi` /
   `jmsx_exec_multi` / `jmsx_bg_start` / `jmsx_bg_check` / `jmsx_put_file` / `jmsx_put_dir` /
   `jmsx_get_file` / `jmsx_relay` / `jmsx_reachable` / `jmsx_session_cost` /
   `jmsx_pool_status` / `jmsx_audit_query`），
   而 **`jmsx_inventory` 只在有 API client 时才注册**（没有 token ⇒
   `Config.APIClient()` 返回 nil ⇒ 它不存在）⇒ 所以计数是
   `want+1`（有凭据）或 `want`（没凭据）。
   ⚠️ **口径**：对外说「16 个工具」；**没有 REST 凭据时那个数字是 15** ——
   别把两个数字说混，也别为了让数字好看去注册一个没凭据也能用的清单工具。
   ⚠️ 断言的是**集合**不是数字：一个改名或删除的工具，光靠计数是发现不了的。

2. **危险命令要 `confirm=true`**，与管理面的 `jms_job_run` 同一标准、
   **同一个分类器**（kit 的 `pkg/risk`，真 shell AST 解析 + 只读命令表）：
   命令被判为 DANGEROUS 才要确认，日常巡检命令不要。
   涉及的工具：`jmsx_exec` / `jmsx_run` / `jmsx_exec_multi` / `jmsx_bg_start` /
   `jmsx_collect` / `jmsx_collect_multi`（**条件确认** ⇒ `confirm` 是可选字段）。
   判为危险却不带标志时，工具**不执行**，而是回一句
   「classified DANGEROUS; re-call with confirm=true」—— 跟 `jms_job_run` 的 plan 同一形状。
   `jmsx_put_file` / `jmsx_put_dir` 例外（覆盖远端文件/目录条目，一律要确认）；
   `jmsx_relay` 也是（它写的是**目标**机器）。
   ⚠️ 条件确认有一个好处：**门禁拒掉的那次调用有留痕**（schema 校验没先把它拒掉）。

3. **`jmsx_exec_multi` 默认并发 4 台**（`concurrency` 可调，1~8）。
   每台机器一条独立录像会话，所以并发不会串数据。实测 6 台机器：串行 54.9s、
   并发 4 **25.5s**（串行时每台都要单独付一次约 10 秒的 koko TUI 握手；
   并发让这些握手重叠）。⚠️ 成本估算里**每台按冷开计价**，别按池命中价。

4. **koko 只认 `ssh-rsa`。** ed25519 公钥堡垒机会「安静地接受」，
   但 koko 一律拒绝 —— 症状是 `Permission denied (password,publickey)`，
   **看起来像凭据错，其实是密钥类型错**。
   `jumpserver-exec --doctor` 的 `key_type` 一项判定私钥的真实类型
   （走 `oekit/network/sshx` 的 `DetectKeyType`，三级回落：直接解析私钥 →
   `ssh-keygen -y` → 文件头嗅探），**提前抓住**这个坑。

5. **终端窗口约 40 行，长输出会截断** —— **只在 TUI 兜底路径上**；
   令牌路径的输出是完整的。要完整输出用 `full=true`。

6. **tmux 会吞掉资产面板里的空格** —— `Exec` 用 `spaceMark` 占位后再还原。
   `debug_spaces.go` 记录了当初怎么定位的，**别删**。

7. **投递状态那一列要照实写。** 多机的那几个工具（`jmsx_exec_multi` /
   `jmsx_collect_multi`）：**调用级**那一格是「任一主机可能还在跑就是 `maybe_sent`」
   （别的机器跑过了，所以不能说「没送到」）；**逐台的**那一格在**答复的每一行**里
   （`delivery`）——「**哪台**可能还在跑」不用去读文字。
   ⚠️ 调用级的 `not_sent` 只在**一台都没跑成、且所有失败都明确说「没送到」**时才有
   （那时整批重试是安全的）；只要有一台跑成了，或有一个失败「没人说」，那一格就是空的。

---

## 4. 验证要求（交付前必须过）

| 检查 | 怎么过 |
|---|---|
| 编译 | `./script/build.sh` 无错（产物落在 `./bin/jumpserver-exec`） |
| 单元测试 | 同脚本内置 `go test ./...`，必须全绿（脚本会打印失败日志，不吞掉） |
| 竞态 | 池的并发护栏在 `internal/repository/koko/pool_test.go` ⇒ **必须带 `-race` 跑** |
| 启动 | `./script/bootstrap.sh` 能起一个进程（stdio 上说 JSON-RPC） |
| 工具数 | **15**（无 REST 凭据时 **14**）—— `internal/service/policy/policy_test.go` 钉住这个**集合** |
| 目录一致 | 策略表与注册表必须一一对应（同上） |
| 契约 | 契约类型必须真被产出（`biz/model/jmsexec/contract_test.go`）；**答案本身必须是契约类型**（`internal/arch/execution_test.go` 的 `TestTheAnswerTypesAreFoundMechanically`） |
| 分层 | 底座不得依赖任何服务；不得 import 管理面；IDL/包/模型三名对齐（`internal/arch/`） |
| **凭据** | 明文凭据不许进仓（底座 `pkg/arch/credential.go` + 本仓 `internal/arch/execution_test.go` 的 `TestThePlaintextCredentialsAreNotInTheRepository`）⇒ 守卫体一份，两个面各接一个入口 |
| HTTP 契约 | `swagger/openapi.json` 与工具目录**逐字节**一致（`openapi_test.go`）；改了工具 / 契约 / `Version` 就跑 `./script/gen-openapi.sh`。「这道守卫会红」由 `script/mutate-swagger-guard.py` 机械证明（对照 + canary + 4 条变异，全红才算过） |
| 自检 | `./bin/jumpserver-exec --doctor` 退出码 0（查密钥类型 / 端口 / 真实 koko 登录） |
| 真调 | 重启会话后实际调一次新工具；**重启前**可用 JSON-RPC 直接驱动已发布二进制（参考 `script/verify-mcp-launch.py`） |

⚠️ **别把「编译过」说成「已完成」。** 汇报时给出：编译结果、测试结果、工具数变化、
发布路径、自检结果、以及「**需要重启会话才生效**」这句话。

⚠️ **动了会话层就要想清楚投递状态**：改「命令怎么发出去」或「失败怎么分类」时，
必须重看 §1 第 1 条 —— **分不清「没投递」与「可能已投递」的后果是同一条命令跑两遍**。

---

## 5. 沙箱速查

| 现象 | 原因 | 修法 |
|---|---|---|
| exit 137、无 stderr | 覆盖写二进制，路径被执行策略拒了 | 发布到新时间戳目录（`script/publish-mcp.py`） |
| `Permission denied (password,publickey)` | **ed25519 密钥**，koko 只认 RSA | `--doctor` 的 `key_type` 一项；换 `ssh-rsa` |
| `replacement directory ... does not exist` | Go module cache 被清 | `./script/restore-go-modcache.sh` |
| 依赖解析要去下载（`go: downloading ...`） | module cache 里少了那个模块 | `./script/restore-go-modcache.sh` |
| `missing go.sum entry for module ...` | go.sum 少了那个精确版本的条目 | `GOFLAGS=-mod=mod GOSUMDB=off GOPROXY=off go build ./...` |
| `go mod tidy` 失败 | 要抓测试专用依赖（x/tools、x/term），沙箱下拿不到 | 别 tidy，手改 go.mod 的 require |
| `-mod may only be set to readonly or vendor` | 模块模式只接受 readonly/vendor | 去掉这个 flag（构建脚本已去掉） |
| 源码树里冒出 `.go.1234567890` 之类文件 | 编辑器/工具留下的备份 | 清理掉，别提交 |
| **每次会话都慢十倍** | 委派在 v4 上回 401 ⇒ 被读成「令牌机制不可用」 | 关掉委派（能力档在 v4 上不发起委派） |
| **底座改了编译不过** | `require` 指向的 kit 版本还没发 | 先发 kit 的 tag，再更新本仓的 require |

---

## 6. 提交约定

- 提交前确认 `git status` 里**没有**构建产物、备份文件、二进制（`./bin/` 不入库）。
- 提交信息用中文，说明**改了什么行为**（不是「更新代码」）。
- ⚠️ **`go.work` / `go.work.sum` 不入库**（见 `.gitignore`）：它是本机的联调接线。
- 不要提交 `~/.minimax/mcp.json` 里的 token —— 那是仓外的运行时配置。
- **改了工具要同步改 `skills/`** —— 那里的工具表是 agent 选工具的依据。

---

## 7. 相关文档

| 文档 | 内容 |
|---|---|
| [`README.md`](README.md) | **对外入口**：定位、前置条件、五分钟装好、安全模型、已知行为 |
| [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) | 环境变量、koko 接线、两条会话路径、连接池、传输与中转、自检、工具速查、故障对照表 |
| [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) | 构建、发布、迭代流程、验证要求、常见坑（**改代码前看这个**） |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | 怎么参与：跑测试、加工具的规矩、提交约定 |
| [`SECURITY.md`](SECURITY.md) | 漏洞报告渠道、凭据处理纪律 |
| [`CHANGELOG.md`](CHANGELOG.md) | 变更记录（Keep a Changelog） |
| [`docs/KOKO-NOTES.md`](docs/KOKO-NOTES.md) | 执行面细节 |
| [`skills/`](skills/) | **给 AI 的操作手册**（jms-exec-ops） |
| **kit 仓的 AGENTS.md** | <https://github.com/yi-nology/jumpserver-kit/blob/main/AGENTS.md> —— **共享纪律**（铁律、底座怎么发版、跨仓守卫） |
| **kit 仓的 ARCHITECTURE.md** | <https://github.com/yi-nology/jumpserver-kit/blob/main/docs/ARCHITECTURE.md> —— 三仓的边界与依赖方向 |

**AI 访问机器一律经堡垒机 koko(2222)**，不接受直连 SSH —— 直连会丢审计录像。

