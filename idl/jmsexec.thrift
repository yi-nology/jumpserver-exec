/**
 * exec —— 执行面的**契约层**。
 *
 * 管理面回答「堡垒机里有什么、谁有权限」，执行面回答「这台机器上现在怎样」。
 * 两个面的形状各自不同，但都用同一套词：thrift IDL → 生成模型 → 工具产出它。
 *
 * 这里定义的是执行面**自己造出来的**那几种形状：
 *
 *   CommandResult   一条命令的结果（stdout、退出码、是否被截断）
 *   ReachableResult 这台机器能不能连上
 *   BatchResult     一条会话里跑的几条命令
 *   CollectResult   一台机器上的一批事实
 *   CollectMultiResult / ExecMultiResult  一次批量作业的结果
 *   SweepPlan       一次批量作业要花多久（演练时给出的那份）
 *   PutFileResult / GetFileResult  文件传输的结果
 *   BgStartResult / BgCheckResult  后台命令的启动与状态
 *   SessionCostResult 一次会话值多少钱
 *
 * 工具目录（名字、风险、确认要求）在共用的 IDL 里（idl/jms/jms.thrift）。
 *
 * 这些形状按域拆成同目录下的多份 thrift（同 namespace，生成同一个
 * Go 包，类型名不变）：
 *
 *   session.thrift   会话的账与连接池
 *   plan.thrift      批量作业的估算
 *   run.thrift       在机器上跑命令：单条、探测、一批、多机
 *   collect.thrift   事实的采集
 *   transfer.thrift  文件的上传、下载与资产间直传
 *   bg.thrift        后台命令的启动与状态
 *   audit.thrift     门禁留痕
 *
 * 本文件是入口：只 include 上面那些文件，本身不再定义类型。
 *
 * 生成方式（slim 模板：不引入 thrift 运行时，只出结构体）:
 *   ./script/gen-idl.sh（thriftgo --gen go:template=slim -r，-r 连同
 *   include 的文件一起生成，缺了它 include 的类型没有生成物）
 */
namespace go jmsexec
include "session.thrift"
include "plan.thrift"
include "run.thrift"
include "collect.thrift"
include "transfer.thrift"
include "bg.thrift"
include "audit.thrift"
