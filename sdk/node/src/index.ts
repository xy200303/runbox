import { createWriteStream } from 'node:fs'
import { readFile } from 'node:fs/promises'
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

export interface FileUpload {
  name: string
  data?: Blob | Uint8Array | string
  path?: string
}

export interface ExecOptions {
  timeoutSeconds?: number
  files?: FileUpload[]
  identity?: Identity
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
      'User-Agent': 'runbox-node/0.2.2',
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

  private async parse<T>(response: Response): Promise<T> {
    if (!response.ok) throw await toError(response)
    if (response.status === 204) return undefined as T
    return (await response.json()) as T
  }

  health(): Promise<Record<string, unknown>> {
    return this.json('GET', '/health')
  }

  capabilities(): Promise<Record<string, unknown>> {
    return this.json('GET', '/v1/capabilities')
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
    const form = new FormData()
    form.set('command', command)
    form.set('timeout_seconds', String(timeout))
    for (const file of files) {
      form.append('files', await fileBlob(file), file.name)
    }
    const response = await this.fetchImpl(this.url('/v1/exec'), {
      method: 'POST',
      headers: this.headers(options.identity),
      body: form,
    })
    return this.parse<ExecResult>(response)
  }

  async upload(name: string, data: Uint8Array | string): Promise<UploadedFile> {
    const [uploaded] = await this.uploadBatch([{ name, data }])
    return uploaded
  }

  async uploadBatch(files: FileUpload[], identity?: Identity): Promise<UploadedFile[]> {
    if (files.length === 0) throw new Error('at least one file is required')
    const form = new FormData()
    for (const file of files) {
      form.append('files', await fileBlob(file), file.name)
    }
    const response = await this.fetchImpl(this.url('/v1/files/batch'), {
      method: 'POST',
      headers: this.headers(identity),
      body: form,
    })
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

  async downloadPath(
    path: string,
    destination: string,
    options: { identity?: Identity; range?: string } = {},
  ): Promise<void> {
    await this.download(
      `/v1/path?path=${encodeURIComponent(path)}`,
      destination,
      options.identity,
      options.range ? { Range: options.range } : undefined,
    )
  }

  async downloadArchive(paths: string[], destination: string, identity?: Identity): Promise<void> {
    if (paths.length === 0) throw new Error('at least one path is required')
    const query = paths.map((path) => `path=${encodeURIComponent(path)}`).join('&')
    await this.download(`/v1/archive?${query}`, destination, identity)
  }

  private async download(
    path: string,
    destination: string,
    identity?: Identity,
    extra?: Record<string, string>,
  ): Promise<void> {
    const response = await this.fetchImpl(this.url(path), {
      headers: this.headers(identity, extra),
    })
    if (!response.ok) throw await toError(response)
    if (!response.body) throw new Error('runner returned an empty body')
    const source = Readable.fromWeb(response.body as never)
    await pipeline(source, createWriteStream(destination))
  }
}

async function fileBlob(file: FileUpload): Promise<Blob> {
  if ((file.path === undefined) === (file.data === undefined)) {
    throw new Error('file must have exactly one of path or data')
  }
  if (file.path !== undefined) {
    const fs = await import('node:fs')
    const openAsBlob = (fs as unknown as { openAsBlob?: (path: string) => Promise<Blob> }).openAsBlob
    if (openAsBlob) return openAsBlob(file.path)
    return new Blob([await readFile(file.path)])
  }
  if (file.data instanceof Blob) return file.data
  return new Blob([file.data as BlobPart])
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
