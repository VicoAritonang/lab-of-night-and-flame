# API contracts

Everything a client, agent or SDK may depend on is defined here, in
language-neutral formats, and the lab is tested against these files.

| File | Interface | Standard |
| --- | --- | --- |
| [`openapi.yaml`](openapi.yaml) | REST API under `/api/v1` | OpenAPI 3.1 (JSON Schema 2020-12) |
| [`openapi.json`](openapi.json) | the same, generated; served at `/api/v1/openapi.json` | OpenAPI 3.1 |
| [`proto/arl/v1`](proto/arl/v1) | gRPC, gRPC-Web and Connect API, package `arl.v1` | Protocol Buffers 3, `buf lint` STANDARD |
| [`asyncapi.yaml`](asyncapi.yaml) | the arl/1 agent protocol over WebSocket | AsyncAPI 3.0 |
| [`gen/arl/v1`](gen/arl/v1) | generated Go messages and Connect clients/handlers | `protoc-gen-go`, `protoc-gen-connect-go` |

REST is the reference interface. The gRPC services are a typed facade over it:
each RPC runs the matching REST operation inside the server, so validation,
project scoping, modules and errors are identical on both. `TelemetryService`
shares the agent connection code with the WebSocket endpoint.

## Using the API from another language

**REST.** Generate a client from `openapi.yaml` with any OpenAPI 3.1 generator,
for example:

```bash
npx @openapitools/openapi-generator-cli generate -i api/openapi.yaml -g java -o clients/java
```

**gRPC / Connect.** Generate stubs from `api/proto` with [buf](https://buf.build)
and the plugins of your language (`buf.gen.yaml` shows the Go setup). The lab
serves all three protocols on its HTTP port: native gRPC needs HTTP/2, which
the server offers without TLS (h2c) and through any TLS proxy that speaks
HTTP/2 to it. Server reflection and `grpc.health.v1` are on:

```bash
grpcurl -plaintext localhost:8000 list
grpcurl -plaintext -d '{"service_name":"support-agent"}' \
  -H 'authorization: Bearer <ARL_AUTH_TOKEN>' localhost:8000 arl.v1.TraceService/ListRuns
# Connect protocol: plain JSON over HTTP/1.1
curl -X POST localhost:8000/arl.v1.LabService/GetHealth -H 'content-type: application/json' -d '{}'
```

Go programs can import the generated package directly:

```go
import (
	"connectrpc.com/connect"
	arlv1 "go.vicoworks.com/agent-reliability-lab/api/gen/arl/v1"
	"go.vicoworks.com/agent-reliability-lab/api/gen/arl/v1/arlv1connect"
)

client := arlv1connect.NewPromptServiceClient(http.DefaultClient, "http://localhost:8000")
res, err := client.GetPrompt(ctx, connect.NewRequest(&arlv1.GetPromptRequest{Name: "support", Label: "production"}))
```

**Agents.** Agents keep using the SDKs in `sdk/` or speak arl/1 directly over
WebSocket (`asyncapi.yaml`, [connection.md](../connection.md)) or over the gRPC
stream `arl.v1.TelemetryService/Connect`.

## Conventions

- **Base path and versions.** REST lives under `/api/v1`. The unversioned
  `/api` prefix is an alias of the newest major version, kept for the bundled
  UI and older SDKs. gRPC packages carry the major version (`arl.v1`).
- **Authentication.** `Authorization: Bearer <ARL_AUTH_TOKEN>` on every
  transport (gRPC metadata `authorization`). Health, auth and the contract
  documents are public.
- **Projects.** `X-Lab-Project` header (gRPC metadata `x-lab-project`),
  default `default`.
- **Field names** are `snake_case` in JSON and protobuf. Times are Unix seconds
  as numbers (`created_at`), except job timestamps, which are RFC 3339.
- **Errors.** REST answers [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457)
  problem documents (`application/problem+json`):

  ```json
  {"type": "urn:arl:problem:feature_disabled", "title": "Not Found", "status": 404,
   "code": "feature_disabled", "detail": "feature \"prompts\" is disabled on this lab; add it to ARL_FEATURES",
   "feature": "prompts", "request_id": "req_..."}
  ```

  `code` is stable and machine-readable; `detail` is for humans. gRPC errors
  carry the same fields in an `arl.v1.ErrorDetail`. Status mapping:

  | REST | `code` | gRPC |
  | --- | --- | --- |
  | 400, 422 | `bad_request`, `validation_failed` | `INVALID_ARGUMENT` |
  | 401 | `unauthenticated` | `UNAUTHENTICATED` |
  | 403 | `forbidden` | `PERMISSION_DENIED` |
  | 404 | `not_found` | `NOT_FOUND` |
  | 404 | `feature_disabled` | `UNIMPLEMENTED` |
  | 409 | `conflict` | `ALREADY_EXISTS` |
  | 413, 429 | `payload_too_large`, `rate_limited` | `RESOURCE_EXHAUSTED` |
  | 503 | `unavailable`, `job_pending` | `UNAVAILABLE` |
  | 500 | `internal` | `INTERNAL` |

- **Request IDs.** Send `X-Request-ID` (1–128 of `A-Z a-z 0-9 . _ : -`) or the
  lab generates one; it is echoed in the response and in problem documents.
- **Idempotency.** `Idempotency-Key` makes creates safe to retry (feedback,
  durable jobs).
- **Durable jobs.** Long operations return `X-Lab-Job-ID`; when the result is
  not ready in time the answer is `503 job_pending`. Retry with the same
  `Idempotency-Key` or poll `GET /api/v1/jobs/{id}` / `arl.v1.JobService/GetJob`.
- **Optional modules.** A module switched off with `ARL_FEATURES` answers
  `404 feature_disabled` (gRPC `UNIMPLEMENTED`); `GET /api/v1/features` lists
  what is on.

## Compatibility promise

Every REST operation is marked `x-stability-level`:

- **`stable`**: system, traces, OTLP ingest, feedback and prompts. Within v1
  these only change additively: new operations, new optional parameters or
  request fields, new response fields, new error codes, new enum values in
  responses. Clients must ignore unknown response fields.
- **`beta`**: experiments, datasets, Workbench, suites, monitor, the rest of
  quality, playground and perf. Shapes may still change between minor
  releases; changes are listed in the release notes. An operation moves to
  `stable` once its shape has settled; it never moves back within v1.

The protobuf package `arl.v1` follows the same rules, enforced by
`buf breaking` (`WIRE_JSON`): no removed or renumbered fields, no changed
types, no renamed services or methods. Field numbers of deleted fields are
`reserved`.

The arl/1 agent protocol only gains optional fields and new event types.

Anything that would break these rules goes to `/api/v2` and `arl.v2`, served
next to v1 for at least one minor release, with v1 marked deprecated
(`Deprecation` and `Sunset` headers, RFC 9745 / RFC 8594).

## How the promise is checked

`go test ./api ./internal/server` (and CI) fails when:

- `openapi.json` is not generated from `openapi.yaml`;
- a route the server registers is missing from `openapi.yaml`, or the spec
  documents a route the server does not serve;
- an operation lacks an `operationId`, tags, a stability level, error
  responses or a declared path parameter, or a `$ref` does not resolve;
- any REST answer seen by the integration tests does not match its schema
  (errors must be problem documents), or an arl/1 frame does not match
  `asyncapi.yaml`;
- the stable surface loses or changes anything recorded in
  [`testdata/stable-v1.json`](testdata/stable-v1.json). Additive changes are
  accepted by re-recording it: `go test ./api -run TestStableContract -update`
  (the diff shows up in review).

CI also runs `buf lint`, `buf format`, `buf breaking` against the previous
commit, and checks that `gen/` is up to date.

## Changing a contract

1. Edit `openapi.yaml` and/or `proto/arl/v1/*.proto` first, then the server.
2. Regenerate: `go generate ./api` (OpenAPI JSON) and `go generate .`
   (`buf generate`, needs [buf](https://buf.build/docs/installation); the
   protoc plugins are pinned in `go.mod` and run with `go tool`).
3. Run `go test ./api ./internal/server`, with `ARL_TEST_DB_URL` set for the
   integration tests that validate real answers.
4. For a stable operation, re-record the lock if the change is additive.
