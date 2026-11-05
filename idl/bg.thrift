/*
 * bg —— 后台命令的启动回执与状态。
 */
namespace go jmsexec

/**
 * 一个后台命令的启动回执（jmsx_bg_start）。
 *
 * 命令是脱离会话跑的（setsid + nohup），所以堡垒机关掉连接它照样活着。
 * exit_marker 是退出码落地的那个文件 —— 状态检查靠它区分「还在跑」和
 * 「跑完了」。
 */
struct BgStartResult {
  1: optional string asset,
  /** 跑的命令。 */
  2: optional string command,
  /** 输出写在哪。 */
  3: optional string output_path,
  /** 退出码写在哪。 */
  4: optional string exit_marker,
  /** 启动命令的输出。 */
  5: optional string status,
  /** 下一步该做什么。 */
  6: optional string next,
  /** 一句话说明这次答复。 */
  7: optional string note,
  /** 风险分级。 */
  8: optional string risk,
  /** 为什么没跑。 */
  9: optional string blocked,
}

/**
 * 一个后台命令的状态（jmsx_bg_check）。
 *
 * state 是 running 还是 finished；finished 时 exit_code 是命令的退出码
 * （字符串，因为它是从机器上的标记文件里读出来的）。
 */
struct BgCheckResult {
  1: optional string asset,
  /** 输出文件的路径。 */
  2: optional string path,
  /** running / finished。 */
  3: optional string state,
  /** 退出码（跑完了才有）。 */
  4: optional string exit_code,
  /** 下一步该做什么。 */
  5: optional string next,
}
