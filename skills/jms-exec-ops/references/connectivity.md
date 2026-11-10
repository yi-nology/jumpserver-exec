# 连通性排障配方（机器视角）

场景：「机器 X 上的应用连不上它依赖的服务」—— 在**那台机器上**探测，用它的视角。
网络问题要问发起方：堡垒机通 ≠ 机器通，机器通 ≠ 应用能连。

`jmsx_reachable` 回答的是另一个问题：「**堡垒机**能不能到机器 X」（开会话前的预检）。
本配方回答「**机器 X** 能不能到它的依赖」。

## 探测梯子（从便宜到贵，逐级收窄）

用 `jmsx_exec`（单条）或 `jmsx_collect`（多条一起带回）在机器上跑。
命令同样要过分类器：本页全部 **2026-11-10 实测**，**没有一条需要 confirm=true**。

### 0. 先看机器上有什么工具

```bash
command -v bash nc curl ping getent ip ss
```

输出是**找到的那些**的路径（没找到的不打印）；列表里有缺失时退出码非零 ——
读**缺了哪个名字**，别把非零退出当成「探测失败」。

### 1. DNS

```bash
getent hosts db.internal.example.com    # READ：有输出=解析成功；空输出=resolver 没给出答案
cat /etc/resolv.conf                    # READ：空输出时追问为什么（nameserver 是谁、search 域）
```

### 2. 路由

```bash
ip route get 10.0.0.5    # MODIFY：会走哪张网卡、哪个网关；报 no route = 路由表缺路由
```

### 3. TCP 端口

```bash
nc -zvw3 10.0.0.5 5432    # MODIFY：nc 存在时语义最准（exit 0 = 端口开着）
```

**通用形态（2026-11-10 真机验证，bridge-home 非 bash 登录 shell 上通过）**：
`/dev/tcp` 魔法由 bash 在**重定向**时处理 —— 裸 `cat /dev/tcp/…` 是把路径当普通文件
参数，**任何机器都不通**（`No such file or directory`，实测）；**显式 bash + 输入重定向**才对：

```bash
timeout 3 bash -c ': < /dev/tcp/10.0.0.5/5432' && echo OPEN    # MODIFY 免确认
```

**三种结局都是明确的**（colon 形态 `:` 立即完成，没有「开着但闲置」的挂起）：

| 结局 | 含义 |
|---|---|
| 打印 `OPEN`（exit 0） | 端口开着（bash 打开套接字成功，`:` 立即完成） |
| 立即 `Connection refused` | 到达主机但端口没监听 → **服务挂了**，不是网络问题 |
| 挂满 3s（exit 124） | connect 没完成 ⇒ SYN 被丢（防火墙 DROP / 路由黑洞） |

**对照口复核 124**（同一台机器上探一个必关端口，如 :1）：

```bash
timeout 3 bash -c ': < /dev/tcp/10.0.0.5/1' && echo OPEN
```

- 对照口**立即拒绝** ⇒ 本机出网路径正常，124 是**这个端口/主机被特殊过滤**
- 对照口**也 124** ⇒ 本机出网路径整体不通（更宽的故障，回第 2 步查路由）

### 4. HTTP（curl）

```bash
curl -sS -o /dev/null -w '%{http_code} %{time_total}\n' --max-time 5 https://example.com/healthz
```

curl 的退出码比 http_code 更先说话（MODIFY，免确认）：

| curl 退出码 | 结论 |
|---|---|
| 0 + code 2xx/3xx | 通，到应用层了 |
| 6 | couldn't resolve —— DNS 问题，回第 1 步 |
| 7 | connection refused —— 到了主机但端口没监听，回第 3 步的「拒绝」行 |
| 28 | timeout —— 中途被丢或对端无响应，回第 3 步的「124」行 |
| 60 | 证书问题（网络已通） —— 加 -k 只为排障确认，别带进生产配置 |
| ≥400 | 网络通，应用层问题 —— 看服务日志（journalctl -u / docker logs） |

### 5. ping（补充证据，不是裁决）

```bash
ping -c 3 -W 2 10.0.0.5    # MODIFY：丢包率 + rtt
```
⚠️ **ping 失败 ≠ 端口不通**：ICMP 可能被过滤 —— ping 全丢但 TCP OPEN 的场景很常见，别拿 ping 当裁决。

### 6. 本机侧证据

```bash
ss -tan | grep 10.0.0.5    # READ：已有连接的状态（ESTAB 正常但新建失败 → conntrack 满/端口耗尽）
```

## 失败分类速查表

按现象查结论，再决定下一步（梯子顺序走完仍定位不了才升级人工）：

| 现象 | 结论 | 下一步 |
|---|---|---|
| getent 空输出 | resolver 配置 / DNS 服务不可达 | 看 /etc/resolv.conf；换 IP 直探第 3 步 |
| ip route get 报 no route | 路由表缺路由 | 联系网络侧，别再重试探测 |
| TCP 立即拒绝 | 服务没监听该端口（服务挂了） | systemctl status / docker ps 看服务，再决定拉起 |
| TCP 124 + 对照口也 124 | 本机出网路径整体不通 | 回第 2 步查路由；找网络侧 |
| TCP 124 + 对照口立即拒绝 | **这个端口/主机被过滤**（防火墙 DROP，不是端口开着） | 找网络侧开白名单；或确认该端口本就该通 |
| curl exit 6/7/28 | 解析失败 / 被拒 / 超时 | 见第 4 步退出码表，逐级回梯子 |
| curl ≥400 | 网络通，应用层问题 | 看服务日志：journalctl -u <svc> / docker logs <c> |
| ping 全丢但 TCP OPEN | ICMP 被过滤，网络其实是通的 | 别改结论，继续按 TCP 证据走 |

## 注意

- 命令分类 **2026-11-10 实测**于 kit v0.12.x；分类器升级后回查本页（尤其 `/dev/tcp` 那条）。
  §3 的通用形态经**真机验证**（bridge-home，2026-11-10）：`bash -c ': < /dev/tcp/…'` 在
  非 bash 登录 shell 的机器上正确给出 OPEN / refused / 124 三态。
  ⚠️ **别改回裸 `cat /dev/tcp/…` 形态** —— 分类器会放行它（READ），但它在**任何机器上都不通**
  （`/dev/tcp` 魔法只在 bash 重定向时生效；分类实测 ≠ 执行实测，这条就是这么漏进来的）。
- 探测是**只读动作**：curl 不落盘、ping 不改机器 —— 但目标由调用方指定，
  批量扫内网端口段不算排障，别把配方当扫描器用。
