/*
 * transfer —— 文件的上传、下载与资产之间的直传。
 */
namespace go jmsexec
include "session.thrift"

/**
 * 一次文件上传的结果（jmsx_put_file）。
 *
 * checksum_match 才是「文件完整到了」的证据：大小对得上不等于内容对得上。
 * 它为 false 时 warning 会说清楚，别拿这个文件。
 */
struct PutFileResult {
  1: optional string asset,
  /** 本地来路（inline content / inline base64 / 本地路径）。 */
  2: optional string local,
  /** 落到机器上的哪个路径。 */
  3: optional string remote,
  /** 传了多少字节。 */
  4: optional i64 bytes,
  /** 分了几片。 */
  5: optional i32 chunks,
  /** 花了多久。 */
  6: optional double seconds,
  /** 机器上算出来的校验和前缀。 */
  7: optional string remote_sha256_prefix,
  /** 传出去的字节的校验和前缀。 */
  8: optional string local_sha256_prefix,
  /** 两边对得上吗。 */
  9: optional bool checksum_match,
  /** 这个会话的账。 */
  10: optional session.SessionStats stats,
  /** 有问题时的提醒。 */
  11: optional string warning,
  /** 设的权限位。 */
  12: optional string mode,
}

/**
 * 一次文件下载的结果（jmsx_get_file）。
 *
 * 窗口是**在远端**切的：offset / length 决定了只传哪一段，没读的字节根本
 * 没离开那台机器。truncated 为 true 表示文件比读回来的多 —— 用
 * available_bytes 知道它到底多大。
 */
struct GetFileResult {
  1: optional string asset,
  /** 读的是机器上哪个文件。 */
  2: optional string remote,
  /** 落到本地的哪个路径（没写本地文件时为空）。 */
  3: optional string local,
  /** 读回来多少字节。 */
  4: optional i64 bytes_returned,
  /** 那个文件一共多大。 */
  5: optional i64 available_bytes,
  /** 从哪个偏移开始读的。 */
  6: optional i64 offset,
  /** 读了多少。 */
  7: optional i64 length,
  /** 是不是只读到了一部分。 */
  8: optional bool truncated,
  /** 花了多久。 */
  9: optional double seconds,
  /** 读回来的字节的校验和前缀。 */
  10: optional string local_sha256_prefix,
  /** 机器上那个文件的校验和前缀。 */
  11: optional string remote_sha256_prefix,
  /** 两边对得上吗（只有整文件读才对得上）。 */
  12: optional bool checksum_match,
  /** 文件内容（没写本地文件时才有）。 */
  13: optional string content,
  /** 有问题时的提醒。 */
  14: optional string warning,
}

/**
 * 一次目录上传的结果（jmsx_put_dir）。
 *
 * 语义是**合并**：tar 解进 remote_dir，同名文件被覆盖，已有但本地没有的
 * 条目原样保留 —— 不是「远端变成和本地一样」。整树替换请用 jmsx_exec
 * 先清目录再传。
 *
 * 完整性证据是 tar 自己的退出码 + 逐文件的本地计数；目录级没有可靠的
 * 「两边逐字节相等」的廉价判据（合并语义下远端本来就多东西）。
 *
 * 演练（dry_run=true，#20）：只做本地预扫（files/bytes/skipped 来自本地
 * walk，lstat 不读内容），**不开会话、不碰堡垒机**——树太大就分片、有
 * skipped 先清理、路径错了当场发现，全部发生在零会话成本里。
 * would_execute=true 表示演练本身没有拒绝什么（预扫完成，confirm 满足后
 * 就会真传）。与命令工具的演练词汇同构（dry_run / would_execute /
 * blocked / risk），演练不是拒绝。
 */
struct PutDirResult {
  /** 目标资产。 */
  1: optional string asset,
  /** 本地来路（被传的目录）。 */
  2: optional string local,
  /** 落到机器上的哪个目录。 */
  3: optional string remote,
  /** 传了多少个文件（目录与软链不计）。 */
  4: optional i32 files,
  /** 文件内容的字节数（不含 tar 结构与块 padding——与 jmsx_put_file 的 bytes 同义）。 */
  5: optional i64 bytes,
  /** 花了多久。 */
  6: optional double seconds,
  /** 这个会话的账。 */
  7: optional session.SessionStats stats,
  /** 一句话说明这次答复是什么（合并语义的提醒在这里）。 */
  8: optional string note,
  /** 被跳过的条目数（socket / 设备文件——tar 流带不动它们）；0 = 没有跳过。 */
  9: optional i32 skipped,
  /** 到达完整性自证：远端 sha256sum -c 核对全部通过 = true；无法核对时是 false + note 说明（绝不假报 true）。 */
  10: optional bool verified,
  /** 远端核对过的文件数。 */
  11: optional i32 verified_files,
  /** 这次答复是演练（本地预扫）：没有开会话，什么都没传。 */
  12: optional bool dry_run,
  /** 演练时：confirm 满足后会不会真的执行。 */
  13: optional bool would_execute,
  /** 演练时为什么不会执行（put_dir 的确认门在门禁层，这里恒为空——预扫完成就是会执行）。 */
  14: optional string blocked,
  /** 工具目录对这个工具的风险分级（destructive：会覆盖机器上的同名文件）。 */
  15: optional string risk,
}

/**
 * 一次目录下载的结果（jmsx_get_dir）。
 *
 * 与 jmsx_put_dir 对称：远端 tar -cf - 的 stdout 走通道回来，本地边收边解。
 * 合并语义：同名文件被覆盖，本地已有但远端没有的条目原样保留。
 * 防御式解包：绝对路径 / 穿越目的地的条目被跳过并计数（skipped），
 * 不会写到 local 之外。
 *
 * 流超时按远端树规模放大（#22）：`du -sk` 探测一次（只走元数据不读内容），
 * 按 put_dir 同一条公式（512KiB/s 地板 + 4x 帽）放大流截止时间；探测失败
 * 则维持调用方基线。失败绝不留「看起来是答案的半棵树」：本调用建的
 * destination 整棵撤掉（报 partial extraction removed），本来就存在的
 * 撤不掉（分不出预存文件）——错误里明说它是部分树，别当完整答案用。
 */
struct GetDirResult {
  /** 目标资产。 */
  1: optional string asset,
  /** 机器上被读的目录。 */
  2: optional string remote,
  /** 落到本地的哪个目录。 */
  3: optional string local,
  /** 收了多少个文件（目录与软链计入 files，字节数只算常规文件）。 */
  4: optional i32 files,
  /** 解出来的常规文件字节数。 */
  5: optional i64 bytes,
  /** 花了多久。 */
  6: optional double seconds,
  /** 跳过的条目数（绝对路径 / 穿越目的地的条目等）；0 = 没有跳过。 */
  7: optional i32 skipped,
  /** 这个会话的账。 */
  8: optional session.SessionStats stats,
  /** 一句话说明这次答复是什么（跳过清单在这里）。 */
  9: optional string note,
}

/**
 * 一次目录级直传的结果（jmsx_relay_dir）。
 *
 * 源端 tar -cf - 的 stdout 直接接目标端 tar -xf - 的 stdin——归档经本进程
 * 的管道对拷，**不落本地磁盘**。目的端合并语义与 jmsx_put_dir 一致。
 * 直通流没有到达自证：note 里说明；需要证明的树请自己跑 sha256 核对，
 * 或改走 GetDir + PutDir 两跳（后者带 sha256 manifest 自证）。
 */
struct RelayDirResult {
  /** 源资产。 */
  1: optional string source_asset,
  /** 源端被读的目录。 */
  2: optional string source_path,
  /** 目标资产。 */
  3: optional string dest_asset,
  /** 目的端解包目录。 */
  4: optional string dest_path,
  /**
   * 归档实际流过的字节数（传输自己的计数）。
   *
   * ⚠️ 它是**流**字节数，tar 结构与 512B 块 padding 都算在内：小树上开销
   * 占比天然高（实测：2 文件 15 字节内容的树报 10240——数字是对的，直觉
   * 是错的），大树上趋近内容大小。别拿它当内容大小比。
   */
  5: optional i64 bytes,
  /** 花了多久。 */
  6: optional double seconds,
  /** 这个会话的账。 */
  7: optional session.SessionStats stats,
  /** 合并语义提醒与「无到达自证」的说明（含 bytes 语义：小树 tar 开销占比高）。 */
  8: optional string note,
}

/**
 * 一次资产之间的直传（jmsx_relay）。
 *
 * 字节从源机器的通道直接进目标机器的通道，**不落本地磁盘** ——
 * 一个从没到过这里的文件，也就不会被留成半个。
 */
struct RelayResult {
  /** 源资产。 */
  1: required string source_asset,
  /** 源机器上的路径。 */
  2: required string source_path,
  /** 目标资产。 */
  3: required string destination_asset,
  /** 目标机器上的路径。 */
  4: required string destination_path,
  /** 传了多少字节。 */
  5: required i32 bytes,
  /** 分几块传的。 */
  6: optional i32 chunks,
  /** 用了多久。 */
  7: optional double seconds,
  /** 源端算出来的校验和。 */
  8: optional string source_sha256_prefix,
  /** 目标端算出来的校验和。 */
  9: optional string destination_sha256_prefix,
  /**
   * 两端的校验和是否一致。
   *
   * 一致 = 文件原样到了；不一致 = 传坏了，别用。
   */
  10: optional bool checksum_match,
  /** 一句话说明这次答复是什么。 */
  11: optional string note,
}
