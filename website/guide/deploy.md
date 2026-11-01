---
title: 部署与加固
description: 卷布局、能力集、反向代理、资源上限，以及 egress 与磁盘配额该放在哪一层。
---

# 部署与加固

Runbox 的部署单位是一个容器加一个数据卷。下面这些是单机部署时真正需要决定的事。

## 数据卷

把 `RUNBOX_ROOT` 挂成一个卷，里面只有两类东西：

```text
/workspace/
├── workspaces/<identity-key>/   各身份的工作区
└── state/
    ├── runbox.db                身份、UID、元数据（SQLite，WAL）
    ├── runbox.db-wal
    └── runbox.db-shm
```

备份两者即可。SQLite 在 WAL 模式，运行中直接拷主文件可能拿到不一致的快照——停容器，或用 SQLite 的备份接口复制。

## 包管理器镜像

沙箱命令的环境是重新构建的白名单，镜像 `ENV` 不会继承进去，所以 pip / npm 的镜像配置写在文件里：

| 文件 | 默认值 | 读取方 |
| --- | --- | --- |
| `/etc/pip.conf` | `https://pypi.tuna.tsinghua.edu.cn/simple` | `pip` |
| `/etc/npmrc` | `https://registry.npmmirror.com` | `npm` |

清华镜像站没有 npm registry，所以 npm 默认用 npmmirror。想换源不必重建镜像：

```bash
docker run ...   -e RUNBOX_PIP_INDEX=https://pypi.org/simple   -e RUNBOX_NPM_REGISTRY=https://registry.npmjs.org
```

runner 会把这两个值注入每条命令的 `PIP_INDEX_URL` 与 `NPM_CONFIG_REGISTRY`，环境变量优先级高于配置文件。要改镜像默认值则用 `--build-arg PIP_MIRROR=... / NPM_MIRROR=...`。

目录权限由 runboxd 在启动时设置并每次重启重新应用：`workspaces/` 是 `0711`（能穿过、不能列举），`state/` 是 `0700`，`runbox.db*` 是 `0600`。不要手动把 `workspaces/` 收紧成 `0700`——沙箱 UID 需要穿过这一层才能到达自己的工作目录，否则每次 `exec` 都会失败并返回 `fork/exec /bin/sh: permission denied`。

## 能力与安全姿态

容器内以 root 运行，才能为不同身份分配 UID 并切换。能力集放到最小：

```yaml
read_only: true
tmpfs:
  - /tmp:rw,exec,nosuid,size=512m
cap_drop: [ALL]
cap_add:
  - CHOWN
  - FOWNER
  - SETUID
  - SETGID
  - DAC_OVERRIDE
  - KILL
security_opt:
  - no-new-privileges:true
```

- 缺任何一个必需能力，启动直接失败，不会半残运行。
- `RUNBOX_ALLOW_MISSING_CAPS=1` 只用于开发，别带进生产。
- 根文件系统只读；`/workspace` 与 `/tmp` 是唯一可写处。
- 镜像用 tini 做 PID 1，不需要额外的 init 容器做回收。

## 资源上限

| 项 | 起点 | 说明 |
| --- | --- | --- |
| CPU | 2 核 | 每次执行是一个 shell 加它的子进程 |
| 内存 | 2 GiB | 两个小 Python 任务或一次 LibreOffice 转换 |
| pids | 256 | 早拦截 fork 炸弹，同时容得下 LibreOffice 的辅助进程 |
| 并发 | `RUNBOX_MAX_CONCURRENCY=2` | 超出返回 `409 busy`，先加副本再谈调高 |

一个身份可以吃满整个容器的资源，目前没有按身份的 cgroup。需要严格公平就按信任域拆容器。

## 反向代理

- 身份头必须由网关注入：剥掉客户端自带的 `X-Runbox-Identity`、`X-Runbox-Namespace`、`X-Runbox-Principal`，再按会话写入唯一一种写法。
- 否则任何持有 bearer token 的调用方都能声明任意身份。
- 上传下载是流式的，代理的超时要按大文件留足；`/v1/archive` 是流式 tar，不要开响应缓冲。
- 容器内也能直接开 TLS：`RUNBOX_TLS_CERT` / `RUNBOX_TLS_KEY`。

## egress 与磁盘配额

这两件事不属于 runner，属于拥有主机的那一层：

**egress**：单机上在网络层收敛——从容器所在网桥丢弃出站流量，只放行工具链需要的包镜像；或者强制走代理（`HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`）。Kubernetes 上用 `NetworkPolicy`。按身份做 egress 规则做不到：所有身份共用一个 network namespace，容器是 egress 控制的单位。

**磁盘**：把专用卷挂在 `RUNBOX_ROOT`，按 `RUNBOX_QUOTA_BYTES × 活跃身份数` 规划容量。`RUNBOX_QUOTA_BYTES` 是执行前的软校验，超了返回 `409 quota_exceeded`，它不会打断正在运行的命令。要硬限额，在主机文件系统上开项目配额（例如 XFS 的 `prjquota`），给每个身份的子目录分配一个 project。

监控上至少盯住：磁盘使用、内存压力、容器重启次数，以及 `409 busy` 的速率。

## 多副本

工作区是本地的。一个负载均衡后面挂多个 runner 时，只有三种选择：

1. 按规范化身份 key 做粘性路由，固定落到同一卷；
2. 每个副本一个卷，按身份亲和；
3. 接受一个身份可能落到空工作区。

不要把同一个 `/workspace` 读写挂给两个 runner：UID 映射、清理任务和 SQLite 写都假设只有一个写者。

## 观测与升级

```bash
curl -fsS http://localhost:8099/ready      # 就绪
curl -fsS http://localhost:8099/metrics \
  -H "Authorization: Bearer $RUNBOX_API_KEY"
```

- `GET /health` 返回版本与运行时长；`/metrics` 是 Prometheus 文本格式。
- `RUNBOX_LOG_FORMAT=json` 时每条请求日志带 `request_id`、方法、路径、状态、字节数和耗时。
- 升级就是换镜像重建容器：停服务时最多等在途请求完成，工作区与 UID 映射都在卷里，不动。

更细的运维清单见仓库里的 `docs/production.md` 与 `docs/deployment.md`。
