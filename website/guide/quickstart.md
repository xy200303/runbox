---
title: 快速开始
description: 五分钟内起一个 Runbox，跑通第一条命令并按路径取回产物。
---

# 快速开始

Runbox 是一个 HTTP 服务：一个容器、一个进程、一个数据卷。下面用 Docker 起一个实例，再用 cURL 或 SDK 调用它。

## 1. 起一个 runner

```bash
export RUNBOX_API_KEY="$(openssl rand -hex 32)"

docker run -d --name runbox \
  --read-only \
  --tmpfs /tmp:rw,exec,nosuid,size=512m \
  --cap-drop ALL \
  --cap-add CHOWN --cap-add FOWNER --cap-add SETUID \
  --cap-add SETGID --cap-add DAC_OVERRIDE --cap-add KILL \
  --security-opt no-new-privileges:true \
  -p 8099:8099 \
  -e RUNBOX_API_KEY="$RUNBOX_API_KEY" \
  -v runbox-data:/workspace \
  ghcr.io/xy200303/runbox:latest
```

```bash
curl -fsS http://localhost:8099/health
# {"status":"healthy","version":"0.2.2","uptime_seconds":2}
```

镜像有三档，接口完全一致，只换 tag：

| Tag | 解包后 | 适合 |
| --- | --- | --- |
| `latest-minimal` | 约 585 MB | 代码、文本、PDF 抽取 |
| `latest` | 约 1.4 GB | Office 文档、表格、pandas/numpy |
| `latest-toolbox` | minimal + 约 300 MB | pip/npm 编译原生扩展 |

## 2. 跑一条命令

身份只有两种写法，二选一，不能同时给：

```bash
curl -fsS http://localhost:8099/v1/exec \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Identity: alice" \
  -H "Content-Type: application/json" \
  -d '{"command":"echo hello from runbox"}'
```

```json
{"exit_code":0,"stdout":"hello from runbox\n","files":[],"duration_ms":3}
```

需要租户 + 用户两层结构时，换成 `X-Runbox-Namespace` 与 `X-Runbox-Principal` 这一对，规则见[身份模型](./identity.md)。

## 3. 产出文件并按路径取回

```bash
curl -fsS http://localhost:8099/v1/exec \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Identity: alice" \
  -H "Content-Type: application/json" \
  -d '{"command":"mkdir -p out && echo hello > out/report.txt"}'
```

响应里的 `files` 会带上工作区相对路径 `out/report.txt`，直接按路径下载：

```bash
curl -fsS "http://localhost:8099/v1/path?path=out/report.txt" \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Identity: alice"
```

目录或一批文件打成一个 tar：

```bash
curl -fsS "http://localhost:8099/v1/archive?path=out&path=tmp/step2" \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Identity: alice" \
  -o artifacts.tar
```

没有 `file_id` 这一层。上传返回路径，产物按路径取，中间文件同样按路径取。

## 4. 用 SDK

```bash
pip install runbox-sdk                     # Python
npm install runbox-sdk                     # Node.js
go get github.com/xy200303/runbox/sdk/go    # Go
```

::: code-group

```python [Python]
from runbox import Client, Identity

client = Client(
    "http://localhost:8099",
    api_key=os.environ["RUNBOX_API_KEY"],
    identity=Identity(namespace="team-a", principal="user-42"),
)
result = client.exec('python3 -c "print(6 * 7)"')
print(result["stdout"])
client.download_path("out/report.csv", "report.csv")
```

```ts [Node.js]
import { Client } from 'runbox-sdk'

const client = new Client({
  baseUrl: 'http://localhost:8099',
  apiKey: process.env.RUNBOX_API_KEY,
  identity: { namespace: 'team-a', principal: 'user-42' },
})
console.log((await client.exec('echo hello')).stdout)
await client.downloadArchive(['out'], 'artifacts.tar')
```

```go [Go]
client, _ := sandbox.NewClient(sandbox.ClientOptions{
    BaseURL:  "http://localhost:8099",
    APIKey:   os.Getenv("RUNBOX_API_KEY"),
    Identity: sandbox.ScopedIdentity("team-a", "user-42"),
})
result, _ := client.Run(ctx, sandbox.ExecRequest{Command: "echo hello"})
```

:::

## 5. 从源码跑

需要 Go 与 Docker。UID 隔离只有 Linux 支持；Windows 和 macOS 只用于开发调试。

```bash
git clone https://github.com/xy200303/runbox.git
cd runbox
cp .env.example .env       # 写入足够长的 RUNBOX_API_KEY
docker compose up -d --build
```

## 下一步

- [身份模型](./identity.md)：两种身份写法、规范化、UID 分配与隔离边界。
- [HTTP 接口](./api.md)：执行、上传、下载、列表与配额。
- [部署与加固](./deploy.md)：卷、能力集、egress 与硬配额怎么做。
