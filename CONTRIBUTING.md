# Contributing

Thanks for helping make AI agents easier to test. Issues and pull requests are welcome.

## Before you start

- For larger changes (new protocol events, new storage, new UI sections) open an
  issue first so we can agree on the design.
- Protocol changes must stay backward compatible within `arl/1`: add optional
  fields and events; never change the meaning of existing ones. Document them in
  [connection.md](connection.md) and update all three SDKs.

## Local setup

| Part | Requirements | Commands |
| --- | --- | --- |
| Server | Go 1.26, PostgreSQL ≥ 14 | `go vet ./... && go test ./...` |
| Integration tests | an **isolated** database initialised with `sql/init.sql` | `ARL_TEST_DB_URL=postgres://…/arl_test go test -p 1 ./...` |
| UI | Node 22 | `cd frontend && npm ci && npm run dev` (proxies `/api` to `:8000`) |
| Go SDK | Go 1.23+ | `cd sdk/go && go test ./...` |
| Python SDK | Python 3.10+ | `cd sdk/python && pip install -e ".[test]" && pytest` |
| TypeScript SDK | Node 22 | `cd sdk/typescript && npm install && npm test` |

Run the whole stack with `docker compose up --build`, or the example agent loop
with `docker compose -f examples/compose/docker-compose.yml up --build`.

Integration tests refuse non-local databases unless `ARL_ALLOW_REMOTE_TEST_DB=true`;
never point them at a database holding real data.

## Releasing (maintainers)

Push a tag `vX.Y.Z` on `main`. The release workflow then:

- tags `sdk/go/vX.Y.Z` (the Go SDK is a nested module in a subdirectory, so its
  versions are `sdk/go/…` tags), then runs
  [.github/scripts/go-sdk-publish.sh](.github/scripts/go-sdk-publish.sh): it checks
  the vanity host, resolves the version through proxy.golang.org, builds a fresh
  consumer module with `go get go.vicoworks.com/agent-reliability-lab/sdk/go@vX.Y.Z`
  and asks pkg.go.dev to index it;
- publishes the Docker image to GHCR and the Python/npm SDKs.

Keep `sdk/python/pyproject.toml`, `sdk/python/src/arl_lab/__init__.py` and
`sdk/typescript/package.json` versions equal to the tag.

To release only the Go SDK, push a tag `sdk/go/vX.Y.Z`, or run the *Go SDK
publish* workflow manually for an existing tag (useful after fixing the vanity
host).

### Go vanity import path

`go.vicoworks.com` is served by a shared vanity worker outside this
repository (`src/packages.js` in the go-vanity project):

```js
"agent-reliability-lab": {
  repo: "https://github.com/VicoAritonang/lab-of-night-and-flame",
  branch: "main",
},
```

It maps the path to the repository root, so the repository holds two modules:

| Module | Directory | Tags |
| --- | --- | --- |
| `go.vicoworks.com/agent-reliability-lab` (server, `internal/…`) | `.` | `vX.Y.Z` |
| `go.vicoworks.com/agent-reliability-lab/sdk/go` (Go SDK) | `sdk/go` | `sdk/go/vX.Y.Z` |

Both resolve through the same meta, served for
`https://go.vicoworks.com/agent-reliability-lab/sdk/go?go-get=1` as well:

```html
<meta name="go-import" content="go.vicoworks.com/agent-reliability-lab git https://github.com/VicoAritonang/lab-of-night-and-flame">
```

`go.mod` lines must match these paths exactly; a major version 2 adds `/v2`
(`…/agent-reliability-lab/sdk/go/v2`) without changes to the vanity worker.

## Pull requests

- Keep changes focused; include tests for behaviour changes.
- `gofmt`, `go vet`, `npm run build` (UI) and the SDK test suites must pass — CI runs them.
- Match the style of the surrounding code.
- Do not commit secrets, `.env` files or captured traces containing customer data.

By contributing you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE).
