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
