---
title: 身份模型
description: Runbox 的两种身份写法、规范化规则、UID 分配与隔离边界。
---

# 身份模型

Runbox 的隔离单位是**身份**。每个请求必须带且只带一种身份写法，服务端把它规范化成一个稳定的 key，这个 key 决定 UID、工作区目录和 SQLite 里的记录。

## 两种写法

| 模式 | 请求头 | 什么时候用 |
| --- | --- | --- |
| 单标识 | `X-Runbox-Identity: <token>` | 调用方已经有一份不透明的用户标识 |
| 分层 | `X-Runbox-Namespace: <namespace>`<br>`X-Runbox-Principal: <principal>` | 需要租户 + 用户两层结构，后续按租户做统计或运营 |

约束：

- 两种写法同时出现，或分层写法只给一半，都返回 `400`。
- 单标识 1–256 个可见 ASCII 字符，不能有空格。
- namespace 与 principal 各 1–128 个字符，字符集 `[A-Za-z0-9._-]`。
- 两种写法都规范化成同一个 SHA-256 key，目录名用这个 key。
- 原始标识不落盘；分层写法里的 namespace/principal 和 UID 分配写在 `state/runbox.db` 的 `identities` 表。

## 规范化之后的目录

```text
/workspace/workspaces/<identity-key>/
├── files/      上传与执行附件落点
├── out/        执行产物
├── .local/     pip --user 安装
├── .npm-global/ npm 全局安装
├── .cache/     缓存
├── .config/    XDG 配置
└── .tmp/       临时文件
```

目录权限 `0750`，属主是该身份的 UID。所有路径解析都相对这个目录，并且对符号链接做安全处理：指向工作区之外的链接不会被跟随。

## UID 分配

- 第一个身份拿到 `20000`，之后递增，映射持久化在 SQLite 里；池默认到 `62000`。
- 容器重启后映射保持不变，工作区里的文件不需要重新归属。
- `DELETE /v1/workspace` 或保留期过期会删除身份行、回收 UID；回收前先杀掉该 UID 的所有进程，避免下一个身份继承上一个身份留下的 `setsid` 守护进程。
- 映射丢失或 UID 区间变化时，下一次执行会把工作区递归 `chown` 到新 UID。
- 命令以该 UID 运行，父进程是 root 的 runner；超时由 runner 用 `KILL` 能力结束整个进程组。

## 隔离边界

同一台机器上的两个身份之间：

| 资源 | 行为 |
| --- | --- |
| 文件 | 各自私有工作区；相对路径相同也不会串。访问别人的路径返回 `404`。 |
| 进程 | 独立进程组；一个身份的命令被超时终止，不会带走另一个身份的命令。 |
| 执行并发 | 同一身份串行，不同身份并发，上限由 `RUNBOX_MAX_CONCURRENCY` 控制。 |
| 安装环境 | 各自 `HOME`、XDG、pip/npm 前缀，互不可见。 |
| 配额 | 每个身份独立计量，超限返回 `409 quota_exceeded`。 |

::: tip 共享内核
所有身份共享容器的内核与 namespace。这是单机轻量的代价，也是它能一个容器跑很多身份的原因。它适合内部或半可信场景；面对互相敌对的租户，把容器整体放到 gVisor 或 microVM 下运行。
:::

## 认证

```http
Authorization: Bearer <RUNBOX_API_KEY>
```

`/health` 与 `/ready` 不需要认证，其余接口都需要。身份头表达的是"以谁的身份操作"，不是认证凭据——持有同一个 bearer token 的调用方可以声明任意身份。因此 reverse proxy 层面必须剥掉客户端自带的身份头，由网关统一注入。
