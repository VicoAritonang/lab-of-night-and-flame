//go:build !arl_nootel

package server

import (
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/otel"
)

const maxOTLPBody = 16 << 20

// otlpTraces implements OTLP/HTTP trace export (protobuf and JSON encodings).
// Point any OpenTelemetry exporter at http://<lab>/api/otel (the exporter
// appends /v1/traces); /v1/traces on the lab root works as well.
func (s *Server) otlpTraces(w http.ResponseWriter, r *http.Request) {
	db := s.DB.Scope(r.Header.Get("X-Lab-Project"))
	if !s.Hub.Listening(db.Project) {
		writeError(w, &HTTPError{503, "the lab is not listening; start listening in Agent lab"})
		return
	}
	var body io.Reader = http.MaxBytesReader(w, r.Body, maxOTLPBody)
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(body)
		if err != nil {
			writeError(w, invalid("invalid gzip body"))
			return
		}
		defer gz.Close()
		body = gz
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxOTLPBody+1))
	if err != nil || len(raw) > maxOTLPBody {
		writeError(w, &HTTPError{413, "OTLP request exceeds 16 MiB"})
		return
	}
	isJSON := strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
	var spans []otel.Span
	if isJSON {
		spans, err = otel.DecodeJSON(raw)
	} else {
		spans, err = otel.DecodeProto(raw)
	}
	if err != nil {
		writeError(w, invalid(err.Error()))
		return
	}
	res, err := otel.Ingest(r.Context(), db, spans)
	if err != nil {
		slog.Warn("OTLP ingestion failed", "spans", len(spans))
		writeError(w, &HTTPError{503, "telemetry could not be persisted; the exporter will retry"})
		return
	}
	for _, id := range res.Finished {
		var count int
		_ = db.Pool.QueryRow(r.Context(), "SELECT event_count FROM arl.traces WHERE id=$1", id).Scan(&count)
		_, _ = s.Q.Scope(db.Project).Enqueue(r.Context(), "trace.summarize", M{"trace_id": id}, fmt.Sprintf("trace:final:%s:%d", id, count))
	}
	message := ""
	if res.Rejected > 0 {
		message = res.Error
	}
	if isJSON {
		out := M{}
		if res.Rejected > 0 {
			out["partialSuccess"] = M{"rejectedSpans": res.Rejected, "errorMessage": message}
		}
		writeJSON(w, 200, out)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(200)
	_, _ = w.Write(otel.PartialSuccess(res.Rejected, message))
}

// otlpInfo documents the endpoint for humans who open it in a browser.
func otlpInfo(r *Request) (any, error) {
	return M{"endpoint": "/api/otel/v1/traces", "encodings": []string{"application/x-protobuf", "application/json"}, "compression": []string{"gzip"}, "conventions": []string{"OpenTelemetry GenAI (gen_ai.*)", "OpenInference", "OpenLLMetry"}, "example_env": M{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://localhost:8000/api/otel/v1/traces", "OTEL_EXPORTER_OTLP_HEADERS": "Authorization=Bearer <ARL_AUTH_TOKEN, if set>"}, "listening": r.S.Hub.Listening(r.DB.Project), "generated_at": domain.Now()}, nil
}
