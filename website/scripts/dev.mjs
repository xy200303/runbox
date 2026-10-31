import { createReadStream, existsSync, statSync } from 'node:fs'
import { createServer } from 'node:http'
import { extname, join, normalize } from 'node:path'
import { dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const port = Number(process.env.PORT || 5173)

const types = {
  '.html': 'text/html; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.txt': 'text/plain; charset=utf-8',
}

createServer((request, response) => {
  const urlPath = decodeURIComponent((request.url || '/').split('?')[0])
  let file = join(root, normalize(urlPath))
  if (!existsSync(file) || statSync(file).isDirectory()) {
    file = join(root, 'index.html')
  }
  response.setHeader('Content-Type', types[extname(file)] || 'application/octet-stream')
  createReadStream(file).pipe(response)
}).listen(port, () => {
  console.log(`runbox website: http://localhost:${port}`)
})
