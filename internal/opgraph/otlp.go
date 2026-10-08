package opgraph

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// OTLPProtobufJSON maps an OTLP/HTTP protobuf trace export, the default of
// most SDKs, to the OTLP JSON mapping DecodeOTLP reads. An export request is
// wire-identical to TracesData (field 1, resource_spans).
func OTLPProtobufJSON(body []byte) ([]byte, error) {
	var traces tracev1.TracesData
	if err := proto.Unmarshal(body, &traces); err != nil {
		return nil, err
	}
	return protojson.MarshalOptions{UseEnumNumbers: true}.Marshal(&traces)
}

// OTLP/HTTP JSON trace export (opentelemetry-proto, JSON mapping): only the
// fields that become observations are decoded; spans are folded and dropped.
type otlpTraces struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []otlpAttribute `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []struct {
				Kind       int             `json:"kind"`
				Start      string          `json:"startTimeUnixNano"`
				End        string          `json:"endTimeUnixNano"`
				Attributes []otlpAttribute `json:"attributes"`
				Status     struct {
					Code int `json:"code"`
				} `json:"status"`
			} `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type otlpAttribute struct {
	Key   string `json:"key"`
	Value struct {
		String *string          `json:"stringValue"`
		Int    *json.RawMessage `json:"intValue"`
	} `json:"value"`
}

// Older semantic-convention spellings that still arrive from deployed SDKs.
var otlpRenamed = map[string]string{
	"http.method": "http.request.method", "http.target": "url.path", "net.peer.name": "server.address", "net.host.name": "server.address",
	"net.host.port": "server.port", "net.peer.port": "server.port", "db.system": "db.system.name", "messaging.destination": "messaging.destination.name",
	"code.function": "code.function.name", "code.filepath": "code.file.path",
	"deployment.environment": "deployment.environment.name",
}

var otlpKinds = map[int]string{1: "internal", 2: "server", 3: "client", 4: "producer", 5: "consumer"}

// Peer keys name the far end of client and producer spans.
var peerKeys = map[string]bool{"server.address": true, "server.port": true, "peer.service": true, "db.system.name": true, "messaging.destination.name": true, "messaging.system": true}

// DecodeOTLP folds one OTLP/HTTP JSON trace export into observations and
// per-service coverage. A service is covered only for the selector keys its
// spans actually carried, so silence on a route it never tags stays unknown.
// A remote export (pushed through the relay) has its own source and never
// passes as local.
func DecodeOTLP(body []byte, now int64, remote bool) ([]Observation, []Coverage, error) {
	var traces otlpTraces
	if err := json.Unmarshal(body, &traces); err != nil {
		return nil, nil, err
	}
	var observations []Observation
	source, spans := "otlp", 0
	if remote {
		source = "otlp-remote"
	}
	// Coverage is per service and the environment its process claimed. The
	// claim stays the process's own, so the source is still otlp.
	type unit struct{ service, environment string }
	keys := map[unit]map[string]bool{}
	for _, resource := range traces.ResourceSpans {
		// Every span counts toward the cap, attributed or not.
		for _, scope := range resource.ScopeSpans {
			if spans += len(scope.Spans); spans > maxObservations {
				return nil, nil, errors.New("otlp export too large")
			}
		}
		resourceAttributes := attributes(resource.Resource.Attributes)
		service, environment := resourceAttributes["service.name"], resourceAttributes["deployment.environment.name"]
		if service == "" {
			continue
		}
		if remote && (environment == "" || environment == "local") {
			environment = "remote"
		} else if environment == "" {
			environment = "local"
		}
		covered := unit{service, environment}
		for _, scope := range resource.ScopeSpans {
			for _, span := range scope.Spans {
				// Only a service that sent spans is covered.
				if keys[covered] == nil {
					keys[covered] = map[string]bool{"service.name": true}
				}
				kind := otlpKinds[span.Kind]
				if kind == "" {
					kind = "internal"
				}
				all := attributes(span.Attributes)
				own, peer := map[string]string{"service.name": service}, map[string]string{}
				for key, value := range all {
					if (kind == "client" || kind == "producer") && peerKeys[key] {
						peer[key] = value
					} else {
						own[key] = value
					}
				}
				for key := range own {
					if selectorKeys[key] && kind != "client" && kind != "producer" {
						keys[covered][key] = true
					}
				}
				start, _ := strconv.ParseInt(span.Start, 10, 64)
				end, _ := strconv.ParseInt(span.End, 10, 64)
				item := Observation{Source: source, Environment: environment, Kind: kind, Start: now, End: now + 1, Attributes: own, Peer: peer, Count: 1}
				if end > start {
					item.LatencyP95 = float64(end-start) / 1e6
				}
				if span.Status.Code == 2 {
					item.Errors = 1
				}
				observations = append(observations, item)
			}
		}
	}
	var coverage []Coverage
	for covered, seen := range keys {
		item := Coverage{Source: source, Environment: covered.environment, Unit: covered.service, AsOf: now, TTL: 15 * 60_000}
		for key := range seen {
			item.Keys = append(item.Keys, key)
		}
		coverage = append(coverage, item)
	}
	return observations, coverage, nil
}

func attributes(values []otlpAttribute) map[string]string {
	result := map[string]string{}
	for _, item := range values {
		key := item.Key
		if renamed, ok := otlpRenamed[key]; ok {
			key = renamed
		}
		switch {
		case item.Value.String != nil:
			result[key] = *item.Value.String
		case item.Value.Int != nil:
			result[key] = string(*item.Value.Int)
			if unquoted, err := strconv.Unquote(result[key]); err == nil {
				result[key] = unquoted
			}
		}
	}
	return result
}

// RunAttribute is the resource attribute factoryd gives every agent run it
// launches (OTEL_RESOURCE_ATTRIBUTES). Agent telemetry is attributed by it
// alone, never by anything a record says about itself.
const RunAttribute = "dark_factory.run.id"

// Agent telemetry quantities. Counts and cost are all that survive decoding.
const (
	TokensIn   = "tokens_in"
	TokensOut  = "tokens_out"
	CostUSD    = "cost_usd"
	ToolCall   = "tool_call"
	APIRequest = "api_request"
)

// AgentPoint is one counted fact an agent CLI exported about a run. Series is
// set only for a cumulative metric point: the identity whose last value the
// caller subtracts.
type AgentPoint struct {
	Run    string
	Kind   string
	Value  float64
	Series string
}

// DecodeAgentLogs counts tool calls and API requests in an OTLP logs export
// (LogsData is wire-identical to the export request). Events are named by
// their event.name attribute (Claude Code "tool_result", Codex
// "codex.tool_result"); bodies and every other attribute are dropped unread.
func DecodeAgentLogs(body []byte, protobuf bool) ([]AgentPoint, error) {
	var data logsv1.LogsData
	if err := unmarshalOTLP(body, protobuf, &data); err != nil {
		return nil, err
	}
	var points []AgentPoint
	for _, resource := range data.GetResourceLogs() {
		run := runOf(resource.GetResource().GetAttributes())
		if run == "" {
			continue
		}
		for _, scope := range resource.GetScopeLogs() {
			for _, record := range scope.GetLogRecords() {
				name := stringAttribute(record.GetAttributes(), "event.name")
				if name == "" {
					name = record.GetEventName()
				}
				switch {
				case strings.HasSuffix(name, "tool_result"):
					points = append(points, AgentPoint{Run: run, Kind: ToolCall, Value: 1})
				case strings.HasSuffix(name, "api_request"):
					points = append(points, AgentPoint{Run: run, Kind: APIRequest, Value: 1})
				}
				if len(points) > maxObservations {
					return nil, errors.New("otlp export too large")
				}
			}
		}
	}
	return points, nil
}

// DecodeAgentMetrics reads token and cost metrics from an OTLP metrics export:
// a name with "token" and "usage" split by its token type attribute (Claude
// Code claude_code.token.usage, Codex codex.turn.token_usage, GenAI
// gen_ai.client.token.usage), and a name with "cost" in USD or, by its
// "microusd" suffix, micro-USD. Sums and histogram sums of either temporality.
func DecodeAgentMetrics(body []byte, protobuf bool) ([]AgentPoint, error) {
	var data metricsv1.MetricsData
	if err := unmarshalOTLP(body, protobuf, &data); err != nil {
		return nil, err
	}
	var points []AgentPoint
	for _, resource := range data.GetResourceMetrics() {
		run := runOf(resource.GetResource().GetAttributes())
		if run == "" {
			continue
		}
		for _, scope := range resource.GetScopeMetrics() {
			for _, metric := range scope.GetMetrics() {
				name, scale, cost := metric.GetName(), 1.0, false
				switch {
				case strings.Contains(name, "cost") && strings.HasSuffix(name, "microusd"):
					scale, cost = 1e-6, true
				case strings.Contains(name, "cost") && metric.GetUnit() == "USD":
					cost = true
				case !strings.Contains(name, "token") || !strings.Contains(name, "usage"):
					continue
				}
				type sample struct {
					attributes []*commonv1.KeyValue
					start      uint64
					value      float64
				}
				var samples []sample
				var temporality metricsv1.AggregationTemporality
				if sum := metric.GetSum(); sum != nil {
					temporality = sum.GetAggregationTemporality()
					for _, point := range sum.GetDataPoints() {
						value := point.GetAsDouble()
						if _, ok := point.GetValue().(*metricsv1.NumberDataPoint_AsInt); ok {
							value = float64(point.GetAsInt())
						}
						samples = append(samples, sample{point.GetAttributes(), point.GetStartTimeUnixNano(), value})
					}
				} else if histogram := metric.GetHistogram(); histogram != nil {
					temporality = histogram.GetAggregationTemporality()
					for _, point := range histogram.GetDataPoints() {
						samples = append(samples, sample{point.GetAttributes(), point.GetStartTimeUnixNano(), point.GetSum()})
					}
				}
				for _, item := range samples {
					kind := CostUSD
					if !cost {
						switch tokenType(item.attributes) {
						case "input":
							kind = TokensIn
						case "output":
							kind = TokensOut
						default:
							continue
						}
					}
					if item.value < 0 {
						continue
					}
					point := AgentPoint{Run: run, Kind: kind, Value: item.value * scale}
					if temporality == metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
						point.Series = seriesKey(name, item.start, item.attributes)
					}
					points = append(points, point)
					if len(points) > maxObservations {
						return nil, errors.New("otlp export too large")
					}
				}
			}
		}
	}
	return points, nil
}

func unmarshalOTLP(body []byte, protobuf bool, message proto.Message) error {
	if protobuf {
		return proto.Unmarshal(body, message)
	}
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, message)
}

func runOf(attributes []*commonv1.KeyValue) string {
	return stringAttribute(attributes, RunAttribute)
}

func stringAttribute(attributes []*commonv1.KeyValue, key string) string {
	for _, item := range attributes {
		if item.GetKey() == key {
			return item.GetValue().GetStringValue()
		}
	}
	return ""
}

func tokenType(attributes []*commonv1.KeyValue) string {
	for _, key := range []string{"type", "token_type", "gen_ai.token.type"} {
		if value := stringAttribute(attributes, key); value != "" {
			return value
		}
	}
	return ""
}

// seriesKey names one cumulative series within a run by a digest, so no
// attribute value (a session or account id) outlives the export.
func seriesKey(name string, start uint64, attributes []*commonv1.KeyValue) string {
	parts := make([]string, 0, len(attributes))
	for _, item := range attributes {
		parts = append(parts, item.GetKey()+"="+item.GetValue().String())
	}
	slices.Sort(parts)
	sum := sha256.Sum256([]byte(name + "\x00" + strconv.FormatUint(start, 10) + "\x00" + strings.Join(parts, "\x00")))
	return string(sum[:])
}
