# Runbox

**Run in a box.**

一个盒子，装下 Agent 的代码和文件——轻量、单容器的执行沙箱，给 AI Agent 用。

Agent 拿到一个可丢弃的 Linux 工作区：上传输入、执行 Python / Node / Shell、下载产物。Runbox 不托管 Agent、不跑模型循环；它是 Agent 调用的命令执行与文件后端。只有一个 Go 二进制、一个容器、一个卷；不挂 `docker.sock`，不需要编排器，也没有数据库。名字和口号就是字面意思：**run in a box**，一个盒子承载多个身份。

[English](README.md)

- **单容器、多身份。** Runbox 不为每个用户新建容器。HTTP runner 与执行环境共处一个长期运行的容器；每个身份用自己的 UID 和独立工作区。
- **按身份隔离，也隔离文件。** 每个身份拥有工作区、配额和工作区之外的不可变归档。同一身份的调用串行，不同身份可并发。
- **所有资源都有边界。** 上传大小、标准输出（保留首尾、内存有上限）、产物数量与大小、工作区配额、归档配额、执行超时、并发数、保留期。
- **文件是一等公民。** 上传、带输入执行、列目录、按不透明 ID 或工作区路径下载、归档一份不可被沙箱改写的长期副本。
- **路径处理防穿越。** 拒绝绝对路径、`..`、盘符和符号链接逃逸；符号链接不会作为产物返回。
- **不泄漏服务端密钥。** 命令只拿到最小环境变量，runner 的 API Key 与进程环境不会被继承。
- **仅标准库的 Go SDK。** 普通调用走 JSON，带附件时自动流式 multipart。

> Runbox 本身不是强多租户边界。它依赖容器 namespace/cgroup 与按 UID 的文件权限。面对互相敌对的租户，请用 gVisor 或 microVM 运行。详见 [docs/security.md](docs/security.md)。

## 为什么是 Runbox

container-per-sandbox 类方案（OpenSandbox、CubeSandbox 的常见部署形态）通常为每个沙箱准备独立容器；microVM 类方案（E2B、microsandbox）更进一步，每个沙箱一台虚拟机。面对互相敌对的租户，这是正确的取舍；但在单机上，这意味着每个用户或任务多一个容器或虚拟机，往往还多一份 Python/LibreOffice。

Runbox 采用 dify-sandbox 的形态：一个长期运行的容器，承载多个身份。每个身份在该容器里拥有稳定 UID 和独立工作区；隔离来自 Linux 凭据、文件权限、路径解析和配额，而不是“每个用户一个容器”。在这个形态之上，Runbox 补充了文件隔离层：按身份的工作区、配额、工作区之外的不可变 `keep/`、符号链接安全解析，以及有界输出通道。

结果是单机友好的部署：一个二进制、一个容器、一个卷、没有调度器。适合内部 Agent 平台，或用户半可信的 SaaS。租户互相敌对时，把 Runbox 放到 gVisor 下，或一个信任域一个容器；不要把共享内核当成 microVM 边界。

### 名字

**Runbox** 就是字面意思：*run in a box*。口号是 **Run in a box.**。box 跑的是 Agent 的代码、装的是 Agent 的文件，不是 Agent 本身；box 是那个单容器，identities 是里面的多个调用方。它刻意不是“每个用户一个容器”，也不是“每个沙箱一台虚拟机”。

## 快速开始

```bash
cp .env.example .env
# 修改 .env，写入足够长的 RUNBOX_API_KEY（openssl rand -hex 32）
docker compose up -d --build
curl -fsS http://localhost:8099/health
```

默认镜像包含 LibreOffice、Python 和 Node。不要 Office 转换能力时构建精简镜像：

```bash
docker build --target minimal -t runbox:minimal .
```

本机开发（Windows / macOS / Linux；只有 Linux 有 UID 隔离）：

```bash
make run
# 或者
RUNBOX_ALLOW_ANONYMOUS=1 RUNBOX_ROOT=./.runbox go run ./cmd/runboxd
```

## 接口一览

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/health` | 存活、版本、运行时长（不鉴权） |
| GET | `/ready` | 就绪：数据目录存在且可写（不鉴权） |
| GET | `/metrics` | Prometheus 指标（需鉴权） |
| GET | `/v1/capabilities` | 限额与检测到的工具 |
| POST | `/v1/exec` | 执行命令（JSON 或 multipart） |
| POST | `/v1/files` | 上传单个文件 |
| GET | `/v1/files/{id}` | 按 ID 下载 |
| POST | `/v1/files/{id}/keep` | 归档为长期文件 |
| GET | `/v1/path?path=out/x` | 按工作区路径下载 |
| GET | `/v1/list?path=out` | 列工作区目录 |
| DELETE | `/v1/workspace[?purge=true]` | 删除工作区（可选连归档一起删） |

身份二选一：

```http
X-Runbox-Identity: 01J...
```

或：

```http
X-Runbox-Namespace: tenant-a
X-Runbox-Principal: user-42
```

同时传两种形式会被拒绝。鉴权：`Authorization: Bearer <key>`。

完整请求/响应说明见 [docs/api.md](docs/api.md)。

## Go SDK

```go
client, _ := sandbox.NewClient(sandbox.ClientOptions{
    BaseURL:   "http://localhost:8099",
    APIKey:    os.Getenv("RUNBOX_API_KEY"),
    Identity: sandbox.ScopedIdentity("team-a", "user-42"),
})
result, err := client.Run(ctx, sandbox.ExecRequest{
    Command: `python3 -c "open('out/report.csv','w').write('a,b\n1,2\n')"`,
    Timeout: 30 * time.Second,
})
for _, file := range result.Files {
    out, _ := os.Create(filepath.Base(file.Name))
    client.DownloadTo(ctx, file.FileID, out)
    out.Close()
}
```

附件直接从 `io.Reader` 流式上传：

```go
result, err := client.Run(ctx, sandbox.ExecRequest{
    Command: "python3 analyze.py files/input.csv",
    Files:   []sandbox.FileUpload{{Name: "input.csv", Reader: file}},
})
```

`Identity` 支持单一 token（`sandbox.TokenIdentity`）或 namespace + principal
（`sandbox.ScopedIdentity`）。两种形式都会规范化为一个 canonical 存储 key；
namespace/principal 会作为元数据保留，供后续租户级操作使用。

`Client` 同时实现 `sandbox.Backend`：每次调用单独传身份时用
`Exec(ctx, identity, …)`；绑定单一身份时用 `Run`。

完整示例：[examples/go](examples/go)。仅标准库的 Python 客户端：[examples/python](examples/python)。curl 走查：[examples/curl.sh](examples/curl.sh)。

## 配置

全部通过环境变量。时长支持 `30s`、`15m`、`7d` 或纯秒数；大小支持 `20MiB`、`20MB`、`1GiB` 或字节数。

| 变量 | 默认值 | 含义 |
|---|---|---|
| `RUNBOX_API_KEY` | —（必填） | 共享 API Key；未开启匿名模式时缺失则拒绝启动。 |
| `RUNBOX_ALLOW_ANONYMOUS` | `false` | 关闭鉴权，仅限本机开发。 |
| `RUNBOX_ALLOW_MISSING_CAPS` | `false` | 允许 root 在缺少 Linux 能力时启动。仅限开发；生产默认 fail closed。 |
| `RUNBOX_ADDR` | `:8099` | 监听地址。 |
| `RUNBOX_ROOT` | `.runbox` | 数据根目录；镜像里设为 `/workspace`。 |
| `RUNBOX_MAX_CONCURRENCY` | `2` | 单 runner 并发命令数；超出返回 `409 busy`。 |
| `RUNBOX_MAX_REQUEST_BYTES` | `200MiB` | HTTP 请求体上限。 |
| `RUNBOX_MAX_UPLOAD_BYTES` | `20MiB` | 单文件上限。 |
| `RUNBOX_MAX_COMMAND_BYTES` | `64KiB` | 单条命令长度上限。 |
| `RUNBOX_MAX_INPUTS` | `64` | 单次执行的输入上限（file ID + multipart 文件）。 |
| `RUNBOX_MAX_STDOUT_BYTES` | `200KiB` | 捕获的 stdout+stderr 上限，保留首尾。 |
| `RUNBOX_MAX_OUTPUT_FILES` | `20` | 单次执行返回的产物数量。 |
| `RUNBOX_MAX_OUTPUT_BYTES` | `200MiB` | 单次执行返回的产物总大小。 |
| `RUNBOX_QUOTA_BYTES` | `1GiB` | 单工作区配额；超出后拒绝执行。`0` 表示不限。 |
| `RUNBOX_KEEP_BYTES` | `1GiB` | 单身份归档配额。`0` 表示不限。 |
| `RUNBOX_RETENTION` | `7d` | 工作区闲置保留期。 |
| `RUNBOX_SWEEP_INTERVAL` | `1h` | 清理任务间隔。 |
| `RUNBOX_UID_MIN` / `RUNBOX_UID_MAX` | `20000` / `20100` | Linux 隔离使用的 UID 池。 |
| `RUNBOX_DEFAULT_TIMEOUT` | `60s` | 请求未指定时的超时。 |
| `RUNBOX_MAX_TIMEOUT` | `300s` | 超时上限，超出会被截断。 |
| `RUNBOX_SHUTDOWN_TIMEOUT` | `30s` | 优雅退出预算。 |
| `RUNBOX_TLS_CERT` / `RUNBOX_TLS_KEY` | — | 进程内终止 TLS；通常更推荐反向代理。 |
| `RUNBOX_LOG_FORMAT` | `text` | `text` 或 `json`。 |

## 工作机制

```
client ──HTTP──► runboxd ──fork/exec──► /bin/sh -c "…"  (UID 20000+n)
                    │                        │
                    ├─ /workspace/workspaces/<identity-key>/  可写，属主为该 UID
                    └─ /workspace/keep/<identity-key>/        只读交付，属主为 runner
```

- 每个身份一个工作区，跨调用保留，安装过的包和中间文件都还在。
- 身份可以是单一 token，也可以是 namespace + principal；两者规范化为同一个 canonical key。scoped 的 namespace/principal 作为元数据保留，供租户级操作；token 不落盘。
- `HOME`、`XDG_*`、pip/npm 前缀都指向工作区，安装结果会保留并计入配额。
- 命令在独立进程组、以该身份 UID 执行；超时只杀进程组，不动容器。
- stdout 使用首尾有界缓冲，命令打印几个 GB 也不会撑爆内存。
- 执行期间写入 `out/` 的文件作为产物返回。交付时复制进 `keep/`，位于可写工作区之外，后续命令改不了。
- 后台清理任务删除超过保留期未活动的工作区；`keep/` 不受影响。

更多细节：[docs/architecture.md](docs/architecture.md)。

## 安全

- 路径解析拒绝 `..`、绝对路径、Windows 盘符和符号链接逃逸。否则 Agent 在 `out/` 里放一个指向 `/etc/shadow` 的链接，root 身份的 runner 就会替它读取。
- 命令不继承 runner 的环境变量。
- compose 配置只读根、`cap_drop: ALL`，仅补 `CHOWN`、`FOWNER`、`SETUID`、`SETGID`、`DAC_OVERRIDE` 和 `KILL`，启用 `no-new-privileges`，并限制内存、CPU、pids。镜像以 tini 作为 PID 1，回收被终止的命令树。
- API Key 常量时间比较；身份只校验不「净化」——净化会把不同身份映射到同一个工作区。
- 归档文件属主是 runner，位于工作区之外，沙箱身份无法改写已交付文件。

已知边界与隔离升级路径（gVisor、microVM、网络策略）：[docs/security.md](docs/security.md)。

## 生产使用

`/ready` 检查数据目录存在、state 目录可写；`/metrics` 暴露有界的 Prometheus 计数器（请求、执行、超时、繁忙、字节数、归档、活跃命令）。以 root 启动时，如果缺少 `CHOWN`、`FOWNER`、`SETUID`、`SETGID`、`DAC_OVERRIDE` 或 `KILL`，进程会直接拒绝启动。

商业部署清单见 [docs/production.md](docs/production.md)：SLO、容量、配额、密钥、gVisor、多副本和故障处理。

## 镜像内容

| 能力 | 完整镜像 | 精简镜像 |
|---|---|---|
| Python 3、Node、npm、git、curl、unzip/zip | 有 | 有 |
| `pdftotext` | 有 | 有 |
| LibreOffice（Writer/Calc/Impress） | 有 | 无 |
| 中文字体 | 有 | 无 |
| Python 文档/数据类库 | 有 | 无 |
| Node `xlsx`、`csv-parse`、`dayjs` | 有 | 无 |

受限网络可用构建参数：`GOPROXY`、`APT_MIRROR`、`PIP_INDEX`、`NPM_REGISTRY`，见 [docs/deployment.md](docs/deployment.md)。

## 方案对比

| | Runbox | container-per-sandbox（OpenSandbox / CubeSandbox） | microVM（E2B / microsandbox） |
|---|---|---|---|
| 隔离单位 | 单容器内 UID + 独立工作区 | 每个沙箱一个容器（或池中一个槽位） | 每个沙箱一台虚拟机 |
| 每个用户新建容器/VM | 否 | 是 | 是 |
| 单机部署占用 | 一个二进制、一个容器、一个卷 | 控制面 + 多个容器 | KVM + 运行时 + VM 镜像 |
| 每个身份的启动成本 | 无容器/VM 启动 | 容器启动或池中取出 | VM 启动或快照恢复 |
| 文件隔离 | 内置：工作区、配额、不可变归档、符号链接防护 | 依赖卷或 API | 强 |
| 适合 | 内部/半可信、单机 | 多机、更强的单沙箱隔离 | 敌对租户 |

OpenSandbox、CubeSandbox 是带控制面的项目，支持不同部署形态；这一列描述的是常见的 container-per-sandbox 模式，不代表其所有模式。gVisor、Dify 的完整对比见 [docs/comparison.md](docs/comparison.md)。

最接近的先例是 **dify-sandbox**：一个容器 + 每任务 UID。Runbox 沿用这个形态，并补上上面说的按身份文件隔离。

## 限制与路线

- 没有内置出网管控；不允许命令访问互联网时请配网络策略/防火墙。
- 不支持 HTTP Range，大文件从头流式下载。
- 工作区是单 runner 本地的。多副本需要按身份做粘性路由或共享存储。
- 没有自定义 seccomp profile，只有 Docker 默认值；下一步建议上 gVisor。
- `keep/` 有单身份配额，但不会自动过期。

## 许可证

MIT。
