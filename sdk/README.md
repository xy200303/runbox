# Runbox SDKs

| Language | Install | Source |
|---|---|---|
| Go | `go get github.com/xy200303/runbox/sdk/go` | [`sdk/go`](go) |
| Python | `pip install runbox-sdk` | [`sdk/python`](python) |
| Node | `npm install runbox-sdk` | [`sdk/node`](node) |

All clients use the same HTTP contract: Bearer authentication, one identity
form (`X-Runbox-Identity` or `X-Runbox-Namespace` + `X-Runbox-Principal`),
streaming uploads, path/archive downloads, and the same JSON errors.

Go lives in the root Go module. Python and Node are packaged independently and
released from `.github/workflows/release.yml`.
