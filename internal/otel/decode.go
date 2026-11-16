// Package otel ingests OpenTelemetry traces (OTLP/HTTP, protobuf or JSON) and
// maps GenAI spans onto arl/1 trace events, so agents instrumented with any
// OpenTelemetry-based framework appear in the lab next to SDK-instrumented ones.
//
// Three attribute dialects are understood: the OpenTelemetry GenAI semantic
// conventions (gen_ai.*), OpenInference (openinference.span.kind, llm.*,
// input.value) and the older OpenLLMetry indexed prompt attributes.
package otel

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

type M = domain.M

// Span is a decoded span in a transport-neutral form. Times are Unix seconds.
type Span struct {
	TraceID, SpanID, ParentID, Name, Service string
	Kind                                     int
	Start, End                               float64
	Attrs                                    M
	Error                                    bool
	StatusMessage                            string
	Events                                   []Event
}

type Event struct {
	Name  string
	Time  float64
	Attrs M
}

const maxString = 32 << 10

// DecodeProto parses an ExportTraceServiceRequest. Only the message types of
// the trace data model are linked; the request wrapper (field 1, repeated
// ResourceSpans) is read directly to avoid pulling in gRPC.
func DecodeProto(body []byte) ([]Span, error) {
	out := []Span{}
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			return nil, errors.New("invalid protobuf")
		}
		body = body[n:]
		if num == 1 && typ == protowire.BytesType {
			raw, m := protowire.ConsumeBytes(body)
			if m < 0 {
				return nil, errors.New("invalid protobuf")
			}
			body = body[m:]
			rs := &tracepb.ResourceSpans{}
			if err := proto.Unmarshal(raw, rs); err != nil {
				return nil, errors.New("invalid ResourceSpans")
			}
			out = append(out, fromProto(rs)...)
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, body)
		if m < 0 {
			return nil, errors.New("invalid protobuf")
		}
		body = body[m:]
	}
	return out, nil
}

func fromProto(rs *tracepb.ResourceSpans) []Span {
	resource := protoAttrs(rs.GetResource().GetAttributes())
	service := domain.Str(resource["service.name"])
	out := []Span{}
	for _, ss := range rs.GetScopeSpans() {
		for _, s := range ss.GetSpans() {
			span := Span{TraceID: hex.EncodeToString(s.GetTraceId()), SpanID: hex.EncodeToString(s.GetSpanId()), ParentID: hex.EncodeToString(s.GetParentSpanId()), Name: s.GetName(), Service: service, Kind: int(s.GetKind()), Start: float64(s.GetStartTimeUnixNano()) / 1e9, End: float64(s.GetEndTimeUnixNano()) / 1e9, Attrs: protoAttrs(s.GetAttributes()), Error: s.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR, StatusMessage: s.GetStatus().GetMessage()}
			for _, e := range s.GetEvents() {
				span.Events = append(span.Events, Event{Name: e.GetName(), Time: float64(e.GetTimeUnixNano()) / 1e9, Attrs: protoAttrs(e.GetAttributes())})
			}
			out = append(out, span)
		}
	}
	return out
}

func protoAttrs(kvs []*commonpb.KeyValue) M {
	out := M{}
	for _, kv := range kvs {
		out[kv.GetKey()] = protoValue(kv.GetValue())
	}
	return out
}

func protoValue(v *commonpb.AnyValue) any {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return clip(x.StringValue)
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue
	case *commonpb.AnyValue_IntValue:
		return float64(x.IntValue)
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue
	case *commonpb.AnyValue_BytesValue:
		return hex.EncodeToString(x.BytesValue)
	case *commonpb.AnyValue_ArrayValue:
		a := []any{}
		for _, item := range x.ArrayValue.GetValues() {
			a = append(a, protoValue(item))
		}
		return a
	case *commonpb.AnyValue_KvlistValue:
		return protoAttrs(x.KvlistValue.GetValues())
	}
	return nil
}

func clip(s string) string {
	if len(s) > maxString {
		return s[:maxString] + "…[truncated]"
	}
	return s
}

// OTLP/JSON differs from protojson: ids are hex, 64-bit integers may be
// strings, enums are integers.
type jsonRequest struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []jsonKV `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []struct {
				TraceID      string          `json:"traceId"`
				SpanID       string          `json:"spanId"`
				ParentSpanID string          `json:"parentSpanId"`
				Name         string          `json:"name"`
				Kind         int             `json:"kind"`
				Start        json.RawMessage `json:"startTimeUnixNano"`
				End          json.RawMessage `json:"endTimeUnixNano"`
				Attributes   []jsonKV        `json:"attributes"`
				Events       []struct {
					Time       json.RawMessage `json:"timeUnixNano"`
					Name       string          `json:"name"`
					Attributes []jsonKV        `json:"attributes"`
				} `json:"events"`
				Status struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"status"`
			} `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type jsonKV struct {
	Key   string    `json:"key"`
	Value jsonValue `json:"value"`
}

type jsonValue struct {
	String *string                       `json:"stringValue"`
	Bool   *bool                         `json:"boolValue"`
	Int    json.RawMessage               `json:"intValue"`
	Double *float64                      `json:"doubleValue"`
	Bytes  *string                       `json:"bytesValue"`
	Array  *struct{ Values []jsonValue } `json:"arrayValue"`
	KVList *struct{ Values []jsonKV }    `json:"kvlistValue"`
}

func DecodeJSON(body []byte) ([]Span, error) {
	var req jsonRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, errors.New("invalid OTLP JSON")
	}
	out := []Span{}
	for _, rs := range req.ResourceSpans {
		resource := jsonAttrs(rs.Resource.Attributes)
		service := domain.Str(resource["service.name"])
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				span := Span{TraceID: normalizeID(s.TraceID), SpanID: normalizeID(s.SpanID), ParentID: normalizeID(s.ParentSpanID), Name: s.Name, Service: service, Kind: s.Kind, Start: nanos(s.Start), End: nanos(s.End), Attrs: jsonAttrs(s.Attributes), Error: s.Status.Code == 2, StatusMessage: s.Status.Message}
				for _, e := range s.Events {
					span.Events = append(span.Events, Event{Name: e.Name, Time: nanos(e.Time), Attrs: jsonAttrs(e.Attributes)})
				}
				out = append(out, span)
			}
		}
	}
	return out, nil
}

// normalizeID accepts the hex ids of the OTLP/JSON spec and, leniently, the
// base64 ids some protojson-based exporters emit.
func normalizeID(s string) string {
	if _, err := hex.DecodeString(s); err == nil {
		return s
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return hex.EncodeToString(b)
	}
	return s
}

// number reads a 64-bit integer encoded as a JSON number or decimal string.
func number(raw json.RawMessage) float64 {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, _ := strconv.ParseFloat(s, 64)
		return n
	}
	var n float64
	_ = json.Unmarshal(raw, &n)
	return n
}

func nanos(raw json.RawMessage) float64 { return number(raw) / 1e9 }

func jsonAttrs(kvs []jsonKV) M {
	out := M{}
	for _, kv := range kvs {
		out[kv.Key] = kv.Value.value()
	}
	return out
}

func (v jsonValue) value() any {
	switch {
	case v.String != nil:
		return clip(*v.String)
	case v.Bool != nil:
		return *v.Bool
	case len(v.Int) > 0:
		return number(v.Int)
	case v.Double != nil:
		return *v.Double
	case v.Bytes != nil:
		b, _ := base64.StdEncoding.DecodeString(*v.Bytes)
		return hex.EncodeToString(b)
	case v.Array != nil:
		a := []any{}
		for _, item := range v.Array.Values {
			a = append(a, item.value())
		}
		return a
	case v.KVList != nil:
		return jsonAttrs(v.KVList.Values)
	}
	return nil
}

// PartialSuccess encodes an ExportTraceServiceResponse reporting rejected spans.
func PartialSuccess(rejected int, message string) []byte {
	if rejected == 0 {
		return []byte{}
	}
	var inner []byte
	inner = protowire.AppendTag(inner, 1, protowire.VarintType)
	inner = protowire.AppendVarint(inner, uint64(rejected))
	inner = protowire.AppendTag(inner, 2, protowire.BytesType)
	inner = protowire.AppendString(inner, message)
	var out []byte
	out = protowire.AppendTag(out, 1, protowire.BytesType)
	return protowire.AppendBytes(out, inner)
}
