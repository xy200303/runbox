import { createReadStream, createWriteStream } from 'node:fs'
import { randomUUID } from 'node:crypto'
import { Readable } from 'node:stream'
import { pipeline } from 'node:stream/promises'

const SCOPED = /^[A-Za-z0-9._-]{1,128}$/
const TOKEN = /^[\x21-\x7E]{1,256}$/

export type Identity =
  | { token: string }
  | { namespace: string; principal: string }

export interface ClientOptions {
  baseUrl: string
  apiKey?: string
  identity?: Identity
  fetch?: typeof fetch
}

/** Any readable source the client can forward without buffering it first. */
export type ByteStream =
  | NodeJS.ReadableStream
  | ReadableStream<Uint8Array>
  | AsyncIterable<Uint8Array>

export interface FileUpload {
  name: string
  data?: Blob | Uint8Array | string
  path?: string
  /** An incoming request body, a stream from another service, or any async iterable. */
  stream?: ByteStream
  contentType?: string
}

/** A streamed download plus the headers needed to forward it unchanged. */
export interface PathStream {
  stream: Readable
  /** Content-Length from the runner, or null when it streams without one. */
  size: number | null
  contentType: string | null
  /** Filename parsed from Content-Disposition, or null. */
  name: string | null
}

export interface ExecOptions {
  timeoutSeconds?: number
  files?: FileUpload[]
  identity?: Identity
}

export interface UploadOptions {
  identity?: Identity
  contentType?: string
}

export interface UploadedFile {
  path: string
  name: string
  size: number
}

export interface ExecResult {
  exit_code: number
  stdout: string
  files: UploadedFile[]
  duration_ms?: number
}

export interface ListEntry {
  name: string
  path: string
  size: number
  is_dir: boolean
  modified: string
}

type StreamInit = RequestInit & { duplex?: 'half' }

export class RunboxError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'RunboxError'
    this.status = status
    this.code = code
  }
}

export function identityHeaders(identity?: Identity): Record<string, string> {
  if (!identity) return {}
  if ('token' in identity) {
    if ('namespace' in identity || 'principal' in identity) {
      throw new Error('use either token or namespace+principal, not both')
    }
    if (!TOKEN.test(identity.token)) {
      throw new Error('token must be 1-256 visible ASCII characters without spaces')
    }
    return { 'X-Runbox-Identity': identity.token }
  }
  if (!SCOPED.test(identity.namespace) || !SCOPED.test(identity.principal)) {
    throw new Error('namespace and principal must match [A-Za-z0-9._-]{1,128}')
  }
  return {
    'X-Runbox-Namespace': identity.namespace,
    'X-Runbox-Principal': identity.principal,
  }
}

export class Client {
  private readonly baseUrl: string
  private readonly apiKey: string
  private readonly identity?: Identity
  private readonly fetchImpl: typeof fetch

  constructor(options: ClientOptions) {
    this.baseUrl = options.baseUrl.replace(/\/+$/, '')
    if (!/^https?:\/\//.test(this.baseUrl)) {
      throw new Error('baseUrl must be an absolute http(s) URL')
    }
    this.apiKey = options.apiKey ?? ''
    this.identity = options.identity
    this.fetchImpl = options.fetch ?? globalThis.fetch
  }

  private headers(identity?: Identity, extra?: Record<string, string>): Headers {
    const headers = new Headers({
      Accept: 'application/json',
      'User-Agent': 'runbox-node/0.2.3',
    })
    for (const [key, value] of Object.entries(identityHeaders(identity ?? this.identity))) {
      headers.set(key, value)
    }
    if (this.apiKey) headers.set('Authorization', `Bearer ${this.apiKey}`)
    for (const [key, value] of Object.entries(extra ?? {})) {
      headers.set(key, value)
    }
    return headers
  }

  private url(path: string): string {
    return this.baseUrl + path
  }

  private async json<T>(method: string, path: string, body?: unknown, identity?: Identity): Promise<T> {
    const response = await this.fetchImpl(this.url(path), {
      method,
      headers: this.headers(identity, body === undefined ? undefined : { 'Content-Type': 'application/json' }),
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    return this.parse<T>(response)
  }

  private async text(path: string, identity?: Identity): Promise<string> {
    const response = await this.fetchImpl(this.url(path), { headers: this.headers(identity) })
    if (!response.ok) throw await toError(response)
    return response.text()
  }

  private async parse<T>(response: Response): Promise<T> {
    if (!response.ok) throw await toError(response)
    if (response.status === 204) return undefined as T
    return (await response.json()) as T
  }

  health(): Promise<Record<string, unknown>> {
    return this.json('GET', '/health')
  }

  ready(): Promise<Record<string, unknown>> {
    return this.json('GET', '/ready')
  }

  capabilities(): Promise<Record<string, unknown>> {
    return this.json('GET', '/v1/capabilities')
  }

  metrics(identity?: Identity): Promise<string> {
    return this.text('/metrics', identity)
  }

  async waitReady(timeoutMs = 30_000, intervalMs = 500): Promise<Record<string, unknown>> {
    const deadline = Date.now() + timeoutMs
    let last: unknown
    for (;;) {
      try {
        return await this.ready()
      } catch (error) {
        last = error
        if (error instanceof RunboxError && error.status !== 503) throw error
      }
      if (Date.now() >= deadline) {
        throw new Error(`runner did not become ready in ${timeoutMs}ms`, { cause: last })
      }
      await new Promise((resolve) => setTimeout(resolve, intervalMs))
    }
  }

  async exec(command: string, options: ExecOptions = {}): Promise<ExecResult> {
    const timeout = options.timeoutSeconds ?? 60
    const files = options.files ?? []
    if (files.length === 0) {
      return this.json<ExecResult>('POST', '/v1/exec', {
        command,
        timeout_seconds: timeout,
      }, options.identity)
    }
    const multipart = multipartBody({ command, timeout_seconds: String(timeout) }, files, 'files')
    const response = await this.fetchImpl(this.url('/v1/exec'), {
      method: 'POST',
      headers: this.headers(options.identity, { 'Content-Type': multipart.contentType }),
      body: multipart.body,
      duplex: 'half',
    } as StreamInit)
    return this.parse<ExecResult>(response)
  }

  async upload(name: string, data: Uint8Array | string): Promise<UploadedFile> {
    const [uploaded] = await this.uploadBatch([{ name, data }])
    return uploaded
  }

  /**
   * Upload from a stream without writing it to disk first.
   *
   * Hand it an incoming request body (`request` in Express), a file stream, a
   * web `ReadableStream` or any async iterable. The request is framed with
   * `Transfer-Encoding: chunked` when the length is unknown.
   */
  async uploadStream(
    name: string,
    stream: ByteStream,
    options: UploadOptions = {},
  ): Promise<UploadedFile> {
    const multipart = multipartBody(
      {},
      [{ name, stream, contentType: options.contentType }],
      'file',
    )
    const response = await this.fetchImpl(this.url('/v1/files'), {
      method: 'POST',
      headers: this.headers(options.identity ?? this.identity, { 'Content-Type': multipart.contentType }),
      body: multipart.body,
      duplex: 'half',
    } as StreamInit)
    return this.parse<UploadedFile>(response)
  }

  async uploadBatch(files: FileUpload[], identity?: Identity): Promise<UploadedFile[]> {
    if (files.length === 0) throw new Error('at least one file is required')
    const multipart = multipartBody({}, files, 'files')
    const response = await this.fetchImpl(this.url('/v1/files/batch'), {
      method: 'POST',
      headers: this.headers(identity, { 'Content-Type': multipart.contentType }),
      body: multipart.body,
      duplex: 'half',
    } as StreamInit)
    const payload = await this.parse<{ files: UploadedFile[] }>(response)
    return payload.files
  }

  async list(path = '', identity?: Identity): Promise<ListEntry[]> {
    const query = path ? `?path=${encodeURIComponent(path)}` : ''
    const payload = await this.json<{ entries: ListEntry[] }>('GET', `/v1/list${query}`, undefined, identity)
    return payload.entries
  }

  async deleteWorkspace(identity?: Identity): Promise<void> {
    await this.json('DELETE', '/v1/workspace', undefined, identity)
  }

  /** Open a workspace file as a Node stream. The caller must consume or destroy it. */
  async openPath(
    path: string,
    options: { identity?: Identity; range?: string } = {},
  ): Promise<Readable> {
    const { stream } = await this.openPathStream(path, options)
    return stream
  }

  /**
   * Open a workspace file together with the headers that describe it. Use this
   * when an HTTP handler forwards the body: set status, Content-Length,
   * Content-Type and Content-Disposition from the result instead of guessing.
   */
  async openPathStream(
    path: string,
    options: { identity?: Identity; range?: string } = {},
  ): Promise<PathStream> {
    const response = await this.fetchImpl(this.url(`/v1/path?path=${encodeURIComponent(path)}`), {
      headers: this.headers(options.identity, options.range ? { Range: options.range } : undefined),
    })
    if (!response.ok) throw await toError(response)
    if (!response.body) throw new Error('runner returned an empty body')
    const length = response.headers.get('content-length')
    return {
      stream: Readable.fromWeb(response.body as never),
      size: length === null ? null : Number(length),
      contentType: response.headers.get('content-type'),
      name: filenameFromDisposition(response.headers.get('content-disposition')),
    }
  }

  /** Open several paths (or a directory) as a tar stream. */
  async openArchive(
    paths: string[],
    options: { identity?: Identity } = {},
  ): Promise<Readable> {
    if (paths.length === 0) throw new Error('at least one path is required')
    const query = paths.map((path) => `path=${encodeURIComponent(path)}`).join('&')
    const response = await this.fetchImpl(this.url(`/v1/archive?${query}`), {
      headers: this.headers(options.identity),
    })
    if (!response.ok) throw await toError(response)
    if (!response.body) throw new Error('runner returned an empty body')
    return Readable.fromWeb(response.body as never)
  }

  async downloadPath(
    path: string,
    destination: string,
    options: { identity?: Identity; range?: string } = {},
  ): Promise<void> {
    const source = await this.openPath(path, options)
    await pipeline(source, createWriteStream(destination))
  }

  /** Download into an open writable stream instead of a path. */
  async downloadPathTo(
    path: string,
    destination: NodeJS.WritableStream,
    options: { identity?: Identity; range?: string } = {},
  ): Promise<void> {
    const source = await this.openPath(path, options)
    await pipeline(source, destination)
  }

  async downloadArchive(paths: string[], destination: string, identity?: Identity): Promise<void> {
    const source = await this.openArchive(paths, { identity })
    await pipeline(source, createWriteStream(destination))
  }

  /** Download a tar archive into an open writable stream. */
  async downloadArchiveTo(
    paths: string[],
    destination: NodeJS.WritableStream,
    identity?: Identity,
  ): Promise<void> {
    const source = await this.openArchive(paths, { identity })
    await pipeline(source, destination)
  }
}

function multipartBody(
  fields: Record<string, string>,
  files: FileUpload[],
  fileField: string,
): { body: ReadableStream<Uint8Array>; contentType: string } {
  const boundary = `----runbox${randomUUID().replace(/-/g, '')}`
  const encoder = new TextEncoder()

  async function* frames(): AsyncGenerator<Uint8Array> {
    for (const [name, value] of Object.entries(fields)) {
      yield encoder.encode(
        `--${boundary}\r\nContent-Disposition: form-data; name="${escapeName(name)}"\r\n\r\n${value}\r\n`,
      )
    }
    for (const file of files) {
      const contentType = file.contentType ?? 'application/octet-stream'
      yield encoder.encode(
        `--${boundary}\r\nContent-Disposition: form-data; name="${fileField}"; filename="${escapeName(file.name)}"\r\nContent-Type: ${contentType}\r\n\r\n`,
      )
      for await (const chunk of chunksOf(file)) yield chunk
      yield encoder.encode('\r\n')
    }
    yield encoder.encode(`--${boundary}--\r\n`)
  }

  const body = Readable.toWeb(Readable.from(frames())) as ReadableStream<Uint8Array>
  return { body, contentType: `multipart/form-data; boundary=${boundary}` }
}

async function* chunksOf(file: FileUpload): AsyncGenerator<Uint8Array> {
  const given = [file.data !== undefined, file.path !== undefined, file.stream !== undefined]
  if (given.filter(Boolean).length !== 1) {
    throw new Error('file must have exactly one of data, path or stream')
  }
  if (file.data !== undefined) {
    if (file.data instanceof Blob) {
      yield new Uint8Array(await file.data.arrayBuffer())
      return
    }
    if (typeof file.data === 'string') {
      yield new TextEncoder().encode(file.data)
      return
    }
    yield file.data
    return
  }
  if (file.path !== undefined) {
    for await (const chunk of createReadStream(file.path)) yield toBytes(chunk)
    return
  }
  const stream = file.stream as ByteStream
  if (isWebStream(stream)) {
    const reader = stream.getReader()
    for (;;) {
      const { done, value } = await reader.read()
      if (done) return
      if (value) yield toBytes(value)
    }
  }
  for await (const chunk of stream as AsyncIterable<unknown>) yield toBytes(chunk)
}

function isWebStream(stream: unknown): stream is ReadableStream<Uint8Array> {
  return typeof (stream as ReadableStream).getReader === 'function'
}

function toBytes(chunk: unknown): Uint8Array {
  if (chunk instanceof Uint8Array) return chunk
  if (typeof chunk === 'string') return new TextEncoder().encode(chunk)
  throw new Error(`unsupported stream chunk: ${typeof chunk}`)
}

function escapeName(value: string): string {
  return value.replace(/\\/g, '_').replace(/"/g, '_').replace(/[\r\n]/g, '_')
}

function filenameFromDisposition(header: string | null): string | null {
  if (!header) return null
  const encoded = /filename\*=UTF-8''([^;]+)/i.exec(header)
  if (encoded) {
    try {
      return decodeURIComponent(encoded[1].trim())
    } catch {
      return encoded[1].trim()
    }
  }
  const plain = /filename="?([^";]+)"?/i.exec(header)
  return plain ? plain[1].trim() : null
}

async function toError(response: Response): Promise<RunboxError> {
  const text = await response.text()
  let code = `http_${response.status}`
  let message = text.trim()
  try {
    const payload = JSON.parse(text) as { code?: string; message?: string }
    code = payload.code ?? code
    message = payload.message ?? message
  } catch {
    // keep the raw body
  }
  if (!message || message.startsWith('<')) message = `HTTP ${response.status}`
  return new RunboxError(response.status, code, message.slice(0, 2048))
}
