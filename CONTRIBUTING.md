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
| Python SDK | Python 3.9+ | `cd sdk/python && pip install -e ".[test]" && pytest` |
| TypeScript SDK | Node 22 | `cd sdk/typescript && npm install && npm test` |

Run the whole stack with `docker compose up --build`, or the example agent loop
with `docker compose -f examples/compose/docker-compose.yml up --build`.

Integration tests refuse non-local databases unless `ARL_ALLOW_REMOTE_TEST_DB=true`;
never point them at a database holding real data.

## Releasing (maintainers)

Push a tag `vX.Y.Z` on `main`. The release workflow then:

- tags `sdk/go/vX.Y.Z` so `go get go.vicoworks.com/arl@vX.Y.Z` works, and warms
  proxy.golang.org / pkg.go.dev;
- publishes the Docker image to GHCR and the Python/npm SDKs.

Keep `sdk/python/pyproject.toml`, `sdk/python/src/arl_lab/__init__.py` and
`sdk/typescript/package.json` versions equal to the tag. The vanity host is a
Cloudflare Worker in [deploy/go-vanity](deploy/go-vanity/README.md).

## Pull requests

- Keep changes focused; include tests for behaviour changes.
- `gofmt`, `go vet`, `npm run build` (UI) and the SDK test suites must pass — CI runs them.
- Match the style of the surrounding code.
- Do not commit secrets, `.env` files or captured traces containing customer data.

By contributing you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE).
