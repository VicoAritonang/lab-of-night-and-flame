// Package api holds the public, versioned contracts of Agent Reliability Lab:
//
//   - openapi.yaml: the REST API under /api/v1 (OpenAPI 3.1). openapi.json is
//     generated from it and served by the lab at /api/v1/openapi.json.
//   - proto/arl/v1: the gRPC/Connect API (package arl.v1); Go code is in gen/.
//   - asyncapi.yaml: the arl/1 agent protocol over WebSocket (AsyncAPI 3.0).
//
// The compatibility rules for all of them are in README.md.
package api

import _ "embed"

//go:generate go run ./internal/openapigen

// OpenAPIYAML is the REST contract as written.
//
//go:embed openapi.yaml
var OpenAPIYAML []byte

// OpenAPIJSON is OpenAPIYAML converted to JSON (go generate ./api).
//
//go:embed openapi.json
var OpenAPIJSON []byte
