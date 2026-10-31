---
title: 验证报告
description: Runbox 的安全与性能实测：140 项攻击断言、深度隔离测试、命令混淆与脚本套娃、并发吞吐与传输性能，以及已知边界。
outline: [2, 3]
---

# 验证报告

Runbox 的隔离是内核属性：执行前切到该身份的 UID，路径解析限制在它的工作区。命令怎么写、编码几次、套几层脚本，都不改变这个边界——下面用可复现的数字说明这一点。

<StatStrip :items="[
  { value: '140', label: '攻击断言', note: '78 常规 · 36 深度 · 26 混淆' },
  { value: '0', label: '越权 / 穿越成功', note: '无跨身份读写，无沙箱逃逸' },
  { value: '8.5 ms', label: 'exec p50', note: 'p95 30.9 ms，max 33 ms' },
  { value: '667 exec/s', label: '16 路并发', note: '超出并发上限立即 409' }
]" />

## 测试方法

| 项 | 值 |
| --- | --- |
| 镜像 | 仓库当前提交构建（含本页列出的全部修复） |
| 运行时 | Docker Desktop，Linux 容器，16 vCPU / 24 GiB |
| 默认限制 | 并发 2、配额 1 GiB、上传 20 MB、stdout 200 KiB、附件 64 |
| 身份 | 每项测试新建身份，避免复用带来的假象 |
| 脚本 | `validation/rbtest.py`、`validation/deep.py`、`validation/obfuscation.py` |

三套脚本都是纯标准库，直连 HTTP，不 mock 服务端。复现方式：

```bash
docker run -d --name rb -p 8099:8099 \
  -e RUNBOX_API_KEY=test-key-123 -e RUNBOX_UID_MAX=40000 \
  -v rb-data:/workspace ghcr.io/xy200303/runbox:latest

RB_PORT=8099 RB_KEY=test-key-123 python validation/rbtest.py security
RB_PORT=8099 RB_CONTAINER=rb python validation/deep.py
RB_PORT=8099 python validation/obfuscation.py
RB_PORT=8099 python validation/rbtest.py perf
```

## 安全测试

### 攻击面覆盖

140 项断言全部按预期拦截，没有一项产生跨身份数据、目录逃逸或提权。

<MeterBars :items="[
  { label: '路径与编码', value: 22, display: '22 / 22 拦截' },
  { label: '身份头', value: 19, display: '19 / 19 拦截' },
  { label: '容器边界', value: 11, display: '11 / 11 拦截' },
  { label: '认证', value: 10, display: '10 / 10 拦截' },
  { label: '身份隔离', value: 9, display: '9 / 9 拦截' },
  { label: '资源上限', value: 4, display: '4 / 4 拦截' },
  { label: 'HTTP 方法', value: 3, display: '3 / 3 拦截' }
]" note="条形长度为该类别断言数；右侧为实际拦截数。另有深度攻击 36 项、混淆与套娃 26 项，同样全部拦截。" />

| 类别 | 覆盖内容 | 结果 |
| --- | --- | --- |
| 认证 | 无 token、错 token、query 传 token、匿名端点白名单 | 未授权请求一律 401 |
| 身份头 | 缺失、两种写法并用、只给一半、非法字符、超长、重复、大小写变体、裸换行 | 一律 400，合法写法正常 |
| 身份隔离 | 跨身份下载、`cat`、目录列举、删工作区、scoped 与 token 混用 | 404 / Permission denied |
| 路径与编码 | `..`、双重编码、反斜杠、绝对路径、NUL、超长路径、全角斜杠、超长 UTF-8 | 400 / 404，零泄漏 |
| 容器边界 | 非 root、只读根、无 sudo、跨 UID 进程、命名空间、cgroup、设备文件 | 全部被内核拒绝 |
| 资源上限 | 上传、命令长度、附件数量、stdout、超时、并发 | 413 / 400 / 409，边界生效 |

### 深度攻击

这些是常规 API 测试覆盖不到的路径，专门针对“看起来能绕过”的地方。

| 攻击 | 手法 | 结果 |
| --- | --- | --- |
| 硬链接搬运 | `ln /etc/shadow out/x` 后下载 | 内核 `protected_hardlinks` 拒绝 |
| 特殊文件阻塞 | `mkfifo out/pipe` 后请求下载 | 先 `Lstat` 判定普通文件，2 ms 内 404 |
| 符号链接竞态 | 3 个读线程 + 1 个换链线程跑 4 秒 | 200 响应 0 次，逃逸 0 次 |
| 归档竞态 | 同一换链过程走 `/v1/archive` | 逃逸 0 次 |
| 进程环境 | 读取 `/proc/1/environ` | Permission denied |
| 进程命令行 | 读取其他身份 `/proc/<pid>/cmdline` | **可见**，已列为已知边界 |
| 内核逃逸 | `unshare -Urm`、`mount`、`chroot` | 全部 `EPERM` |
| 运行时后门 | `/var/run/docker.sock`、`/dev/mem` | 不存在 |
| 请求走私 | multipart 里塞两个 `command` 字段 | 400，第二段不执行 |
| 参数污染 | `?path=a&path=../../etc/shadow` | 取第一个，不逃逸 |
| 归档放大 | 1001 个 `path` 参数 | 400 |
| UID 复用 | 删工作区后新建身份 | 新身份从空工作区开始 |

### 命令混淆与脚本套娃

这一组专门回答“把恶意代码藏起来会不会绕过”。结论是不会：鉴权发生在 `fork/exec` 之前，与命令文本无关。

| 混淆方式 | 载荷 | 结果 |
| --- | --- | --- |
| Base64 | `echo … \| base64 -d \| sh` | `/etc/shadow` Permission denied |
| 十六进制 + eval | `printf … \| xxd -r -p \| sh` | 同样拒绝（镜像无 `xxd`，退化为 shell 报错） |
| 三级脚本套娃 | a 写 b，b 写 c，c 携带越权载荷 | 读不到状态库，也读不到其他工作区 |
| 落盘脚本 | 写入 `out/x.sh` 后 `sh` / `bash` 执行 | 边界不变 |
| 路径拼接 | 变量、`printf`、花括号、引号拼接、命令替换、here-doc | 8 种写法无一读到 `/etc/shadow` |
| 动态拼装逃逸 | `unshare`、`chroot` 由变量拼出 | 仍是 `EPERM` |
| setuid 拷贝 | `cp /bin/sh` 后 `chmod 4755` 再执行 | 生效 UID 仍是沙箱 UID（如 20005） |
| 批量重试 | 40 次读 shadow、20 次走符号链接 | 全部拒绝 |

### 身份隔离矩阵

| 资源 | 同身份 | 其他身份 |
| --- | --- | --- |
| 工作区文件 | 读写正常 | `404`（API）/ `Permission denied`（Shell） |
| 相同相对路径 | 各自内容 | 互不可见 |
| 目录列举 | 正常 | `0711`，列举被拒 |
| 进程信号 | 可结束自己的 | `Operation not permitted` |
| 身份数据库 | 不适用 | `0600`，不可读 |
| 删除工作区 | 只影响自己 | 不影响他人 |

## 性能测试

测试对象是最小可用路径：单容器、默认并发 2、本地卷。数字来自 16 vCPU 机器上的 Docker Desktop，实际部署会受磁盘与 CPU 影响。

### 并发吞吐

<LineChart :points="[
  { label: '2 路', value: 67 },
  { label: '4 路', value: 367 },
  { label: '8 路', value: 493 },
  { label: '16 路', value: 667 }
]" unit=" exec/s" note="不同身份并行执行 `true`。同一身份严格串行；超出 RUNBOX_MAX_CONCURRENCY 时立即返回 409 busy，不排队。" />

### 命令启动开销

<MeterBars :items="[
  { label: 'exec true', value: 8.5, display: '8.5 ms' },
  { label: '新身份首次', value: 20.2, display: '20.2 ms' },
  { label: 'python3 -c pass', value: 38.2, display: '38.2 ms' },
  { label: 'node -e 0', value: 196.8, display: '196.8 ms' }
]" max="220" note="p50 值。开销主要在解释器启动，Runbox 自身的调度与鉴权约 8 ms。" />

### 文件传输

<MeterBars :items="[
  { label: '上传 1 MB', value: 29.9, display: '29.9 MiB/s' },
  { label: '上传 10 MB', value: 40.8, display: '40.8 MiB/s' },
  { label: '上传 19 MB', value: 60.4, display: '60.4 MiB/s' },
  { label: '下载 32 MB', value: 66.4, display: '66.4 MiB/s' }
]" max="75" note="全链路流式：上传边读边落盘，下载边读边发，Range 请求直接落到文件偏移。" />

### 明细

| 指标 | 结果 |
| --- | --- |
| `exec true` | p50 8.5 ms · p95 30.9 ms · max 33 ms |
| 新身份首次执行（建 UID + 工作区） | p50 20.2 ms |
| 16 路并发 | 667 exec/s |
| 同身份 3 × `sleep 1` | 3.03 s（严格串行） |
| 超出并发（8 个 `sleep 2`，上限 2） | 6 个立即 409，2 个执行 |
| Range 读 1 MB（32 MB 文件） | 46 ms，`206` + `Content-Range` |
| 500 个小文件 | 列举 10 ms，tar 打包 60 ms |
| 一次 exec 带 20 个附件 | 30 ms |

## 已知边界

严格测试的另一半是承认边界。以下不是缺陷，是单容器共享内核模型的取舍，已在部署文档中给出缓解方式。

| 边界 | 影响 | 缓解 |
| --- | --- | --- |
| 跨身份可见命令行 | `/proc/<pid>/cmdline` 世界可读，`ps` 能看到别人的命令参数 | 不要把密钥放 argv；gVisor / 单租户一容器 / `hidepid=2` |
| 共享 `/tmp` | 容器内 `/tmp` 对所有身份可写 | 敏感任务不落 `/tmp`，定期重建容器 |
| 后台进程 | `setsid` 守护进程在请求结束后继续运行 | 删除或过期工作区时会被回收 |
| 软配额 | 单条命令仍可能超出配额 | 用 `DELETE /v1/path` 释放，或主机层做硬配额 |
| 无自定义 seccomp | 依赖 Docker 默认 profile | 需要更强边界时使用 gVisor |
| 共享内核 | 容器级隔离，不是 microVM | 敌对租户请把容器整体放进 gVisor 或 microVM |

## 结论

- 140 项攻击断言、0 项越权成功；隔离与命令文本无关，混淆和套娃不改变结果。
- 单次执行 8.5 ms，16 路并发 667 exec/s，传输 66 MiB/s，足以支撑单机多租户的 Agent 工作负载。
- 所有边界都以可复现脚本给出，脚本在仓库 `validation/` 下，可自行重跑。
