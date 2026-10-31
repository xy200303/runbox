import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
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
    if (request.url === '/v1/files/batch') {
      response.writeHead(200, { 'Content-Type': 'application/json' })
      response.end(JSON.stringify({ files: [{ path: 'files/a.txt', name: 'a.txt', size: 3 }] }))
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
    const { writeFile } = await import('node:fs/promises')
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
