//go:build arl_nootel

package server

import (
	"net/http"

	"go.vicoworks.com/agent-reliability-lab/internal/features"
)

// Built with -tags arl_nootel: the OTLP decoder (protobuf) is left out and the
// otel module reports itself as not compiled.
func (s *Server) otlpTraces(w http.ResponseWriter, r *http.Request) {
	writeError(w, s.unavailable(features.OTel))
}
func otlpInfo(r *Request) (any, error) { return nil, r.S.unavailable(features.OTel) }
