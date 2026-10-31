# Runbox Node SDK

TypeScript client for [Runbox](https://runbox.xyun.dev), the lightweight sandbox
for agent commands and files. ESM only, typed, and built on `fetch` + Node
streams: uploads and downloads never pass through memory unless you ask them to.

```bash
npm install runbox-sdk
```

## Quick start

```ts
import { Client } from 'runbox-sdk'

const client = new Client({
  baseUrl: 'http://localhost:8099',
  apiKey: process.env.RUNBOX_API_KEY,
  identity: { namespace: 'team-a', principal: 'user-42' },
})

const result = await client.exec('python3 -c "print(6 * 7)"', { timeoutSeconds: 30 })
console.log(result.exit_code, result.stdout) // 0 42
```

Requests are stateless; the same client is safe to share across concurrent
calls, and any method accepts an `identity` override.

## Identity

Exactly one identity form per request:

| Form | Value | Headers sent |
| --- | --- | --- |
| Opaque token | `{ token: 'user-42' }` | `X-Runbox-Identity` |
| Tenant + user | `{ namespace: 'team-a', principal: 'user-42' }` | `X-Runbox-Namespace` + `X-Runbox-Principal` |

`identityHeaders()` is exported if your own code needs to build the same
headers for a raw fetch.

## Run a command

```ts
const result = await client.exec('python3 analyze.py files/input.csv', {
  timeoutSeconds: 60,
  files: [{ name: 'input.csv', path: './input.csv' }],
})
```

Each `FileUpload` has exactly one source: `data` (bytes or string), `path`
(streamed from disk) or `stream` (an `AsyncIterable<Uint8Array>`, a web
`ReadableStream`, or a Node stream — for example `request` from an incoming
Express handler). Attachments land in `files/<name>` inside the workspace.

## Files

```ts
const uploaded = await client.upload('notes.txt', 'hello')
await client.uploadStream('big.bin', createReadStream('./big.bin')) // any ByteStream
await client.uploadBatch([{ name: 'a.csv', path: './a.csv' }])
const entries = await client.list('out')
await client.deleteWorkspace() // keeps the identity and its UID
```

Downloads stream to a path, to a writable stream, or straight to the caller:

```ts
await client.downloadPath('out/report.csv', 'report.csv')
await client.downloadPathTo('out/report.csv', response)   // response: http.ServerResponse
await client.downloadArchive(['out', 'tmp'], 'artifacts.tar')
```

## Streaming and forwarding

`openPath` returns a `Readable`. `openPathStream` also returns the headers, so a
proxy can answer with the right status, length, type and filename:

```ts
const { stream, size, contentType, name } = await client.openPathStream('out/report.pdf', {
  range: req.headers.range,
})

res.writeHead(req.headers.range ? 206 : 200, {
  'Content-Type': contentType ?? 'application/octet-stream',
  ...(size !== null ? { 'Content-Length': String(size) } : {}),
  ...(name ? { 'Content-Disposition': `attachment; filename="${name}"` } : {}),
})

pipeline(stream, res, (error) => {
  if (error && error.code !== 'ERR_STREAM_PREMATURE_CLOSE') throw error
})
```

A whole directory as one tar stream:

```ts
const archive = await client.openArchive(['out', 'tmp/step2'])
res.writeHead(200, {
  'Content-Type': 'application/x-tar',
  'Content-Disposition': 'attachment; filename="artifacts.tar"',
})
pipeline(archive, res, () => {})
```

`pipeline` is what makes disconnects cheap: when the client goes away, the
destination errors, the source is destroyed, and the runner sees the closed
connection and stops copying.

## Health and limits

```ts
await client.health()
await client.ready()
await client.capabilities()          // limits and detected tooling
await client.waitReady(30_000)       // polls /ready
await client.metrics()               // Prometheus text
```

## Errors

Non-2xx responses throw `RunboxError` with `status`, `code` and `message`:

```ts
import { RunboxError } from 'runbox-sdk'

try {
  await client.exec('rm -rf out && echo gone')
} catch (error) {
  if (error instanceof RunboxError && error.code === 'quota_exceeded') {
    await client.deleteWorkspace()
  } else {
    throw error
  }
}
```

Common codes: `400` bad identity or path, `401` bad bearer token, `404` path
missing or owned by another identity, `409` `busy` / `quota_exceeded`, `413`
request too large.

## Requirements

Node 18 or newer (Node 20+ recommended: `fs.openAsBlob` is used for
file-backed uploads when available). Ships ESM with TypeScript types; no
runtime dependencies.

## Links

- API reference: [docs/api.md](../../docs/api.md)
- Deployment and hardening: [docs/deployment.md](../../docs/deployment.md)
- Changelog: [CHANGELOG.md](../../CHANGELOG.md)
