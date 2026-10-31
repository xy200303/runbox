# Runbox website

Static product site for [runbox.xyun.dev](https://runbox.xyun.dev).

```bash
npm install     # no dependencies; kept for convention
npm run dev     # http://localhost:5173
npm run build   # writes website/dist
```

The build is a dependency-free static copy: `index.html`, `src/` and
`public/` are emitted into `dist/`. GitHub Actions deploys `dist/` to Pages.

`public/CNAME` pins the custom domain. DNS for `runbox.xyun.dev` is a CNAME to
`xy200303.github.io`.
