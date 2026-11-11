# K8s 配方：跳板机 kubectl 路径

K8s 平台的资产**开不出 shell**：koko 拒它们的 exec 通道（2026-11-11 对 v5
实测——连接令牌签得出、SSH 登录得过，但命令类工具拿到的是「exit 0 +
空输出」的假成功；未加平台门的版本还会把它报成 reachable=true 的假绿灯）。
执行面现在在开会话之前按平台明确拒绝，答复里带本页路径。

**K8s 的正确打开方式是跳板机 kubectl**：挑一台装了 kubectl 的受管机器
（SSH 资产），对它跑 `jmsx_exec`——会话与审计照走堡垒机。

**链路 2026-11-11 全链路实测全绿**：经 home-k8s-2（控制面节点 SSH 资产）跑
`k3s kubectl get nodes` —— exit 0、全输出、留痕在案。
⚠️ **首调慢是链路抖，不是坏**：冷开实测 17.7s，链路抖时 45s。

## 1. 资产布局

| 角色 | 是什么 | 怎么用 |
|---|---|---|
| K8s 平台资产 | 堡垒机上 `platform=Kubernetes` 的记录 | **不是命令目标**——命令类工具（exec/run/collect/exec_multi/collect_multi）与文件传输都会被平台门拒绝 |
| 跳板机 | 装了 kubectl 的受管机器（SSH 资产，如控制面节点） | 命令目标：`jmsx_exec {"asset":"<跳板机>","command":"kubectl get nodes"}` |

找跳板机用 `jmsx_inventory`（`platform` 参数按平台过滤）。

## 2. 巡检

分类器对照表（2026-11-12 对 kit 分类器实测；kit#28/#29/#31 落地后的档位）：

| 形态 | 分类 | 说明 |
|---|---|---|
| `kubectl get nodes` / `get pods` / `get pod -o wide` | READ | 巡检主力 |
| `kubectl get pods -A` / `-n prod` | READ | 旗标归位（kit#29）——此前落 MODIFY，只是标签 |
| `kubectl get events --sort-by=.lastTimestamp` | READ | 集群事件按时间排 |
| `kubectl top nodes` / `kubectl top pods` | READ | 资源水位 |
| `kubectl describe node <名>` | READ | 单节点详情 |
| `kubectl logs <pod> -n <ns>` | READ | 容器日志（`--tail`/`-f`/`-p` 都在只读表） |
| `kubectl config current-context` | READ | 看当前指向 |
| `kubectl auth can-i create pods` | READ | 权限自检 |
| `k3s kubectl get nodes` | **MODIFY（免确认）** | `k3s` 外壳不在只读表——能跑，标签不准；kubectl 进 PATH 后用裸 `kubectl` |
| `kubectl apply -f x.yaml` | MODIFY（免确认） | 变更下发 |
| `kubectl config use-context prod` | **MODIFY** | 不再顶 READ 档（kit#29）——它改 kubeconfig、决定后续打哪个集群；**配方避开改 context**，确需时先 `config current-context` 记下原值 |
| `kubectl delete pod <名>` | **DANGEROUS** | 要 `confirm=true` |
| `kubectl exec <pod> -- <载荷>` | 按载荷判 | 危险载荷 DANGEROUS（kit#26），温和载荷 MODIFY 免确认 |
| `kubectl run <名> -- <载荷>` | 按载荷判 | 同 exec——kit#28 把 run 缺口关了，**不再有免确认的 run 载荷** |
| `kubectl apply --prune -f m/` | **DANGEROUS** | 删清单外现存资源（kit#31） |

一次采集多台跳板（或一台跳板上的多个事实）见 SKILL.md 第 3 节；
标准巡检检查集见 [`health-check.md`](health-check.md)（主机侧指标）。

## 3. 排障

| 症状 | 命令（全部 READ 免确认） |
|---|---|
| Pod 不起来 | `kubectl get pods -A --field-selector status.phase!=Running`、`kubectl describe pod <名> -n <ns>` |
| 服务异常 | `kubectl logs <pod> -n <ns> --tail 100`、`kubectl logs <pod> -p`（上一次崩溃的） |
| 节点异常 | `kubectl describe node <名>`（Conditions 段）、`kubectl top nodes` |
| 事件流 | `kubectl get events -A --sort-by=.lastTimestamp` |
| 调度去哪了 | `kubectl get pod -o wide`（NODE 列） |

## 4. 多集群 / 多机

- **多台跳板并发采集**：`jmsx_collect_multi` / `jmsx_exec_multi` 对多台
  跳板机并发跑同一组 kubectl 事实——每台一条独立录像会话，先
  `dry_run=true` 看 plan（几轮/多久），底数是实测的。
- **多集群**：一个集群一套跳板机资产；集群间的差异用 collect 的标签列
  并排看。**不要**用 `config use-context` 在一台跳板上切集群（改的是
  kubeconfig 本体，切完忘切回来，下一个人就打到错的集群）。

## 5. 注意

- **K8s 平台资产本身不能当命令目标**：现在会拿到明确拒绝（error 带
  kubectl 指引、`delivery=not_sent`），不是假成功。
- **TUI 兜底的 40 行截断**对长输出同样生效：`kubectl get pods -A` 在大
  集群上轻松超窗——`via` 里没有 connection token 时长输出要 `full=true`，
  或者一开始就收窄（`--field-selector`、`| head`）。
- 退出码是原生的（令牌路径）：`kubectl` 的非零退出码就是 `exit_code`。

## 6. 凭据链（可选段）

home-k8s 已落一套可参照的标准配置：堡垒机 k8s 资产账号 +
**ServiceAccount token + view 级 RBAC**（最小权限：get/list/describe/top
够巡检，不带写），SA 的标签 `app.kubernetes.io/managed-by=jumpserver-exec-probe`。
新集群照抄这套：先建 SA → 绑 view 级 Role → 取 token 挂到堡垒机的 k8s
资产账号上，再从跳板机 kubectl 验证 `auth can-i`。
