---
title: HTTP 接口
description: Runbox 的 HTTP 接口一览：执行、上传、按路径下载、tar 批量下载、列表与配额。
---

# HTTP 接口

所有接口都在 `:8099` 上，认证是 `Authorization: Bearer <RUNBOX_API_KEY>`，身份头见[身份模型](./identity.md)。

## 一览

| Method | Path | 用途 |
| --- | --- | --- |
| `GET` | `/health` | 存活、版本、运行时长（免认证） |
| `GET` | `/ready` | 就绪：数据目录存在且可写（免认证） |
| `GET` | `/metrics` | Prometheus 指标 |
| `GET` | `/v1/capabilities` | 限额与已检测到的工具 |
| `POST` | `/v1/exec` | 执行命令（JSON 或 multipart） |
| `POST` | `/v1/files` | 上传单个文件，返回工作区路径 |
| `POST` | `/v1/files/batch` | 上传多个文件，返回各自路径 |
| `GET` | `/v1/path?path=out/x` | 按工作区路径下载 |
| `DELETE` | `/v1/path?path=out/x` | 删除工作区内的一个路径（超配额时的逃生口） |
| `GET` | `/v1/list?path=out` | 列出工作区目录 |
| `GET` | `/v1/archive?path=out&path=tmp` | 文件或目录打包成 tar 下载 |
| `DELETE` | `/v1/workspace` | 删除该身份的工作区 |

## 执行

```http
POST /v1/exec
Content-Type: application/json

{"command":"python3 -c 'print(6*7)'"}
```

```json
{
  "exit_code": 0,
  "stdout": "42\n",
  "stderr": "",
  "files": [],
  "duration_ms": 128
}
```

带附件时用 `multipart/form-data`：字段 `command` 是命令，重复出现的 `files` 字段是附件，落到 `files/<name>`，命令里直接按这个路径引用。

```bash
curl -fsS http://localhost:8099/v1/exec \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Identity: alice" \
  -F "command=python3 analyze.py files/input.csv" \
  -F "files=@./input.csv" \
  -F "files=@./params.json"
```

边界：命令长度、超时、stdout/stderr 采集量、产物数量与单个大小、附件数量（`RUNBOX_MAX_ATTACHMENTS`）都有上限，超出返回 `413` 或截断标记。

## 上传

单个文件用字段名 `file`：

```bash
curl -fsS http://localhost:8099/v1/files \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Identity: alice" \
  -F "file=@./report.xlsx"
# {"path":"files/report.xlsx","bytes":48213}
```

多个文件用重复的 `files` 字段：

```bash
curl -fsS http://localhost:8099/v1/files/batch \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Identity: alice" \
  -F "files=@./a.csv" -F "files=@./b.csv"
```

上传边读边落盘，不会把整个请求体读进内存。

## 下载

单个文件按相对路径：

```bash
curl -fsS "http://localhost:8099/v1/path?path=out/report.csv" \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Identity: alice" \
  -o report.csv
```

支持 `Range` 与 `HEAD`，适合大文件分段与断点续传。

目录或一批路径打成 tar：

```bash
curl -fsS "http://localhost:8099/v1/archive?path=out&path=tmp/step2" \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Identity: alice" \
  -o artifacts.tar
```

`/v1/path` 与 `/v1/archive` 都只接受相对路径，`..` 和指向工作区外部的符号链接会被拒绝。

工作区超配额时所有命令都会返回 `409 quota_exceeded`，连 `rm` 也执行不了。此时用 `DELETE /v1/path?path=out` 直接删目录，或 `DELETE /v1/workspace` 重置整个工作区。

## 列表与清理

```bash
curl -fsS "http://localhost:8099/v1/list?path=out" \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Identity: alice"
```

```bash
curl -fsS -X DELETE http://localhost:8099/v1/workspace \
  -H "Authorization: Bearer $RUNBOX_API_KEY" \
  -H "X-Runbox-Identity: alice"
```

删除工作区不会删除身份与 UID 映射；下次调用会重建一个空工作区。

## 错误

| 状态码 | 场景 |
| --- | --- |
| `400` | 身份头缺失、冲突或格式不合法 |
| `401` | bearer token 缺失或错误 |
| `404` | 路径不存在，或属于别的身份 |
| `409` | `busy`（并发已满）、`timeout`（超时已杀）、`quota_exceeded`（超配额，用 `DELETE /v1/path` 释放）或 `uid_exhausted`（UID 池用尽） |
| `413` | 请求体、附件或产物超过上限 |

错误响应是 JSON，带 `error` 字段与可读说明；`409` 会区分 `busy` 与 `quota_exceeded`，方便调用方决定是重试还是清理。
