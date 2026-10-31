# Runbox Node SDK

```bash
npm install runbox-sdk
```

```ts
import { Client } from 'runbox-sdk'

const client = new Client({
  baseUrl: 'http://localhost:8099',
  apiKey: process.env.RUNBOX_API_KEY,
  identity: { namespace: 'team-a', principal: 'user-42' },
})

const result = await client.exec('echo hello')
console.log(result.stdout)
```

Uploads use `FormData` and stream file-backed `Blob`s where Node supports
`fs.openAsBlob`; downloads stream to a destination path.

Full API reference: [docs/api.md](../../docs/api.md).
