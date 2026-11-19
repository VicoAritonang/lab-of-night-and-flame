package server

import (
	"net/http"
)

// contract serves one of the published API documents (package api).
func contract(contentType string, doc []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(doc)
	}
}

const docsPage = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Agent Reliability Lab API</title>
<style>body{font:16px/1.55 system-ui,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem}code{font-size:.92em}</style>
<h1>Agent Reliability Lab API</h1>
<p>Stable contract, version 1. Every interface below is generated from the same definitions in the
repository's <code>api/</code> directory, so clients in any language can be generated from them.</p>
<ul>
<li><b>REST</b> under <code>/api/v1</code>: <a href="/api/v1/openapi.yaml">OpenAPI 3.1 (YAML)</a> · <a href="/api/v1/openapi.json">JSON</a>.
Errors are RFC 9457 <code>application/problem+json</code> with a stable <code>code</code>.</li>
<li><b>gRPC, gRPC-Web and Connect</b> on this port: package <code>arl.v1</code> (<code>api/proto/arl/v1</code>).
Server reflection and <code>grpc.health.v1</code> are enabled, e.g. <code>grpcurl -plaintext localhost:8000 list</code>.</li>
<li><b>arl/1 agent protocol</b> over WebSocket <code>/api/v1/agent-lab/ws</code> or the gRPC stream
<code>arl.v1.TelemetryService/Connect</code>: <a href="/api/v1/agent-lab/connection.md">integration guide</a>.</li>
<li><b>OpenTelemetry</b>: OTLP/HTTP traces at <code>/v1/traces</code>.</li>
</ul>
<p>Authenticate with <code>Authorization: Bearer &lt;ARL_AUTH_TOKEN&gt;</code> and select a project with
<code>X-Lab-Project</code>. Long operations return a durable job ID in <code>X-Lab-Job-ID</code>.</p>
`
