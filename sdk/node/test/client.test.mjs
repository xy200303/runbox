import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { PassThrough } from 'node:stream'
import { Readable } from 'node:stream'
import test from 'node:test'

import { Client, RunboxError, identityHeaders } from '../dist/index.js'

async function startServer() {
  const requests = []
  const server = createServer(async (request, response) => {
    const chunks = []
    for await (const chunk of request) chunks.push(chunk)
    const body = Buffer.concat(chunks)
    requests.push({ method: request.method, url: request.url, headers: request.headers, body })

    if (request.url === '/v1/exec') {
      response.writeHead(200, { 'Content-Type': 'application/json' })
      response.end(JSON.stringify({ exit_code: 0, stdout: 'ok', files: [] }))
      return
    }
    if (request.url === '/v1/files' || request.url === '/v1/files/batch') {
      response.writeHead(200, { 'Content-Type': 'application/json' })
      response.end(JSON.stringify({ path: 'files/a.txt', name: 'a.txt', size: 3, files: [{ path: 'files/a.txt', name: 'a.txt', size: 3 }] }))
      return
    }
    if (request.url.startsWith('/v1/path')) {
      if (request.url.includes('missing')) {
        response.writeHead(404, { 'Content-Type': 'application/json' })
        response.end(JSON.stringify({ code: 'not_found', message: 'not found' }))
        return
      }
      response.writeHead(200, { 'Content-Type': 'application/octet-stream' })
      response.end('path-data')
      return
    }
    if (request.url.startsWith('/v1/archive')) {
      response.writeHead(200, { 'Content-Type': 'application/x-tar' })
      response.end('tar-data')
      return
    }
    if (request.url === '/metrics') {
      response.writeHead(200, { 'Content-Type': 'text/plain' })
      response.end('runbox_uptime_seconds 1\n')
      return
    }
    if (request.url === '/ready') {
      response.writeHead(200, { 'Content-Type': 'application/json' })
      response.end(JSON.stringify({ status: 'ready' }))
      return
    }
    response.writeHead(404, { 'Content-Type': 'application/json' })
    response.end(JSON.stringify({ code: 'not_found', message: 'not found' }))
  })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  const { port } = server.address()
  return { server, requests, baseUrl: `http://127.0.0.1:${port}` }
}

test('identity headers', () => {
  assert.deepEqual(identityHeaders({ token: 'alice' }), { 'X-Runbox-Identity': 'alice' })
  assert.deepEqual(identityHeaders({ namespace: 'team-a', principal: 'user-42' }), {
    'X-Runbox-Namespace': 'team-a',
    'X-Runbox-Principal': 'user-42',
  })
  assert.throws(() => identityHeaders({ token: 'a b' }))
})

test('exec sends auth and identity', async () => {
  const { server, requests, baseUrl } = await startServer()
  try {
    const client = new Client({
      baseUrl,
      apiKey: 'test-key',
      identity: { namespace: 'team-a', principal: 'user-42' },
    })
    const result = await client.exec('echo hello')
    assert.equal(result.stdout, 'ok')
    assert.equal(requests[0].headers.authorization, 'Bearer test-key')
    assert.equal(requests[0].headers['x-runbox-namespace'], 'team-a')
    assert.equal(requests[0].headers['x-runbox-principal'], 'user-42')
  } finally {
    server.close()
  }
})

test('batch upload sends file content', async () => {
  const { server, requests, baseUrl } = await startServer()
  const directory = await mkdtemp(join(tmpdir(), 'runbox-node-'))
  try {
    const path = join(directory, 'a.txt')
    await writeFile(path, 'aaa')
    const client = new Client({ baseUrl, apiKey: 'test-key', identity: { token: 'alice' } })
    const uploaded = await client.uploadBatch([{ name: 'a.txt', path }])
    assert.equal(uploaded[0].path, 'files/a.txt')
    assert.match(requests[0].body.toString(), /aaa/)
  } finally {
    await rm(directory, { recursive: true, force: true })
    server.close()
  }
})

test('uploadStream forwards a request body without buffering it', async () => {
  const { server, requests, baseUrl } = await startServer()
  try {
    const client = new Client({ baseUrl, apiKey: 'test-key', identity: { token: 'alice' } })
    const uploaded = await client.uploadStream(
      'report.csv',
      Readable.from([Buffer.from('forwarded-'), Buffer.from('bytes')]),
      { contentType: 'text/csv' },
    )
    assert.equal(uploaded.path, 'files/a.txt')
    const request = requests[0]
    assert.equal(request.url, '/v1/files')
    assert.equal(request.headers['transfer-encoding'], 'chunked')
    assert.equal(request.headers['content-length'], undefined)
    const body = request.body.toString()
    assert.match(body, /filename="report.csv"/)
    assert.match(body, /Content-Type: text\/csv/)
    assert.match(body, /forwarded-bytes/)
  } finally {
    server.close()
  }
})

test('exec forwards an attachment stream', async () => {
  const { server, requests, baseUrl } = await startServer()
  try {
    const client = new Client({ baseUrl, apiKey: 'test-key', identity: { token: 'alice' } })
    await client.exec('cat files/input.txt', {
      files: [{ name: 'input.txt', stream: Readable.from(['stream-body']) }],
    })
    const body = requests[0].body.toString()
    assert.match(body, /name="command"/)
    assert.match(body, /stream-body/)
  } finally {
    server.close()
  }
})

test('download path and archive', async () => {
  const { server, baseUrl } = await startServer()
  const directory = await mkdtemp(join(tmpdir(), 'runbox-node-'))
  try {
    const client = new Client({ baseUrl, apiKey: 'test-key', identity: { token: 'alice' } })
    const pathFile = join(directory, 'path.bin')
    const archiveFile = join(directory, 'archive.tar')
    await client.downloadPath('out/report.txt', pathFile)
    await client.downloadArchive(['out'], archiveFile)
    assert.equal((await readFile(pathFile)).toString(), 'path-data')
    assert.equal((await readFile(archiveFile)).toString(), 'tar-data')
    await assert.rejects(
      client.downloadPath('missing', join(directory, 'missing.bin')),
      (error) => error instanceof RunboxError && error.code === 'not_found',
    )
  } finally {
    await rm(directory, { recursive: true, force: true })
    server.close()
  }
})

test('downloadPathTo pipes into an open writable', async () => {
  const { server, baseUrl } = await startServer()
  try {
    const client = new Client({ baseUrl, apiKey: 'test-key', identity: { token: 'alice' } })
    const sink = new PassThrough()
    const chunks = []
    sink.on('data', (chunk) => chunks.push(chunk))
    await client.downloadPathTo('out/report.txt', sink)
    assert.equal(Buffer.concat(chunks).toString(), 'path-data')
  } finally {
    server.close()
  }
})

test('openArchive streams and metrics works', async () => {
  const { server, baseUrl } = await startServer()
  try {
    const client = new Client({ baseUrl, apiKey: 'test-key', identity: { token: 'alice' } })
    const archive = await client.openArchive(['out'])
    const chunks = []
    for await (const chunk of archive) chunks.push(chunk)
    assert.equal(Buffer.concat(chunks).toString(), 'tar-data')
    assert.match(await client.metrics(), /runbox_uptime_seconds/)
    assert.equal((await client.waitReady(2000, 100)).status, 'ready')
  } finally {
    server.close()
  }
})

test('openPathStream forwards the range and returns metadata', async () => {
  let gotRange = null
  const server = createServer((request, response) => {
    gotRange = request.headers.range ?? null
    response.writeHead(206, {
      'Content-Type': 'application/pdf',
      'Content-Length': '4',
      'Content-Disposition': 'attachment; filename="report.pdf"',
    })
    response.end('part')
  })
  await new Promise((resolve) => server.listen(0, resolve))
  const { port } = server.address()
  try {
    const client = new Client({
      baseUrl: `http://127.0.0.1:${port}`,
      apiKey: 'test-key',
      identity: { token: 'alice' },
    })
    const { stream, size, contentType, name } = await client.openPathStream('out/report.pdf', {
      range: 'bytes=0-3',
    })
    const chunks = []
    for await (const chunk of stream) chunks.push(chunk)
    assert.equal(Buffer.concat(chunks).toString(), 'part')
    assert.equal(gotRange, 'bytes=0-3')
    assert.equal(size, 4)
    assert.equal(contentType, 'application/pdf')
    assert.equal(name, 'report.pdf')
  } finally {
    server.close()
  }
})
