# go.vicoworks.com — Go vanity import paths

A Cloudflare Worker that makes `go get go.vicoworks.com/arl` resolve to
`github.com/VicoAritonang/lab-of-night-and-flame` (directory `sdk/go`).

| Request | Response |
| --- | --- |
| `GET /arl?go-get=1` (also `/arl/...`) | `<meta name="go-import" content="go.vicoworks.com/arl git https://github.com/VicoAritonang/lab-of-night-and-flame sdk/go">` + `go-source` |
| `GET /arl` from a browser | redirect to `https://pkg.go.dev/go.vicoworks.com/arl` |
| `GET /` | list of modules |
| `GET /healthz` | `ok` |

The subdirectory field of `go-import` is understood by the go command since
**Go 1.25**. Users on older toolchains still resolve the module through the
default `GOPROXY` (proxy.golang.org runs a current Go), so only `GOPROXY=direct`
on Go < 1.25 is unsupported. Because the module lives in a subdirectory, its
version tags are `sdk/go/vX.Y.Z` (the release workflow creates them).

## Deploy (once, from your machine)

```bash
cd deploy/go-vanity
npm install
npx wrangler login          # opens the browser; pick the account that owns vicoworks.com
npm test
npx wrangler deploy         # creates the Worker and the go.vicoworks.com custom domain
curl "https://go.vicoworks.com/arl?go-get=1"
```

If `go.vicoworks.com` already has a DNS record (it currently answers 404),
delete that record in the Cloudflare dashboard first: a Worker custom domain
creates its own DNS record and certificate.

## Deploy from GitHub Actions

`.github/workflows/go-vanity.yml` deploys on every push to `main` that touches
this folder (or manually via *Run workflow*). Add repository secrets:

- `CLOUDFLARE_API_TOKEN` — token from the *Edit Cloudflare Workers* template,
  plus *Zone → DNS → Edit* for `vicoworks.com` (needed for the custom domain).
- `CLOUDFLARE_ACCOUNT_ID` — from the Workers overview page.

## Adding another module

Append to `MODULES` in [src/index.js](src/index.js) (`path`, `repo`, `subdir`
for nested modules, `branch`, `description`), add a test, and deploy. The
module's `go.mod` must declare `module go.vicoworks.com/<path>`.
