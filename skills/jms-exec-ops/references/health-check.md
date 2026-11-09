# 健康巡检配方（负载 / 内存 / 磁盘 / TCP / 进程）

给 agent 的标准巡检：一次调用采齐负载、内存、磁盘、TCP、进程、系统信息。
输出是原始文本，但每一项的**读法与阈值**都在本页 —— 照着判，不用二次摸索。

工具：单机 `jmsx_collect`，多机 `jmsx_collect_multi`（矩阵：行=机器，列=事实）。
`checks` 里的命令要过分类器，本页全部命令 **2026-11-10 实测**过分类：
**没有任何一条需要 confirm=true**（READ 或 MODIFY 免确认）。

## 1. 标准检查集

**核心 7 项**（一次采集全带上）：

| 名字 | 命令 | 实测分类 |
|---|---|---|
| host | `hostname` | READ |
| load | `cat /proc/loadavg` | READ |
| cores | `nproc` | MODIFY（免确认） |
| mem | `free -m` | READ |
| disk | `df -hP \| grep -vE 'tmpfs\|devtmpfs\|overlay\|squashfs'` | READ |
| tcp | `ss -s` | READ |
| proc | `ps axo pid,comm,pcpu,pmem --sort=-pcpu \| head -6` | MODIFY（免确认） |

**扩展 3 项**（TCP 状态明细 / 系统版本 / inode，默认可不带）：

| 名字 | 命令 | 实测分类 |
|---|---|---|
| tcp_states | `ss -tan \| awk 'NR>1{print $1}' \| sort \| uniq -c` | MODIFY（免确认） |
| os | `uname -sr` | READ |
| inode | `df -hPi \| grep -vE 'tmpfs\|devtmpfs'` | READ |

## 2. 判读阈值

| 信号 | 读法 |
|---|---|
| load1 vs cores | load1 > cores = 过载；load5/15 同高 = 持续过载，只有 load1 高 = 瞬时尖峰 |
| mem available 列 | available < 10% of total = 内存吃紧（看 available，不看 free —— free 含可回收缓存） |
| disk use% | >85% = 紧张（规划扩容/清日志）；>95% = 危急（磁盘满会开始挂服务） |
| inode IUse% | >80% = 小文件洪水（磁盘还有空间但创建文件会失败） |
| ss -s 的 TCP 计数 | ESTAB 突增 = 连接堆积；TIME-WAIT 多 = 短连接轮转（量大但通常良性） |
| CLOSE-WAIT 堆积 | **应用 bug 信号**：对端已关、本端不 close() —— 查应用，不是查网络 |
| proc 列表 | pcpu 高且 comm 是刚部署的服务 = 新代码在烧 CPU；pmem 高 = 查泄漏 |
| error 行 | **error ≠ 没数据**：某台 error 了要看 error 内容（连不上 ≠ 空载），别把它并进 fleet 对比 |

## 3. 跑法

单机一次采齐：

```bash
jmsx_collect {"asset":"node-031","checks":[<标准检查集>]}
```

多机矩阵，先演练拿计划（几轮/多久），再正式跑：

```bash
# 演练：不开会话，报「几轮、大概多久」（plan 字段）
jmsx_collect_multi {"assets":["node-031","rpi4-011","dnsbox1"],"checks":[<标准检查集>],"dry_run":true}

# 正式跑（并发默认 4，可调 1~8）
jmsx_collect_multi {"assets":[...],"checks":[<标准检查集>]}
```

## 4. 注意

- 检查集是**配方的一部分**：本页每条命令 2026-11-10 对 kit v0.12.x 分类器实测；
  分类器升级（尤其 bashguard 上游补 sed/awk 脚本解析后）**回查一遍再发新会话**。
- 多机时每台**一条会话**，9 条命令 ≈ 2~3s/台（令牌路径）；大范围先 `dry_run=true` 看 plan（几轮/秒数）再跑。
- 某台 `error` ≠ 采到空数据：连接失败、超时各自有 error 文本 —— 判读时按台看，别把失败并进对比。
- **跳板机上有的工具各机器不一定有**：`nc` 缺失时用 `timeout 3 cat /dev/tcp/<host>/<port>`（READ，见连通性配方）。
