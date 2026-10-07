package opgraph

import (
	"encoding/json"
	"errors"
	"strconv"

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
func DecodeOTLP(body []byte, now int64) ([]Observation, []Coverage, error) {
	var traces otlpTraces
	if err := json.Unmarshal(body, &traces); err != nil {
		return nil, nil, err
	}
	var observations []Observation
	// Coverage is per service and the environment its process claimed. The
	// claim stays the process's own, so the source is still otlp.
	type unit struct{ service, environment string }
	keys := map[unit]map[string]bool{}
	for _, resource := range traces.ResourceSpans {
		resourceAttributes := attributes(resource.Resource.Attributes)
		service, environment := resourceAttributes["service.name"], resourceAttributes["deployment.environment.name"]
		if service == "" {
			continue
		}
		if environment == "" {
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
				item := Observation{Source: "otlp", Environment: environment, Kind: kind, Start: now, End: now + 1, Attributes: own, Peer: peer, Count: 1}
				if end > start {
					item.LatencyP95 = float64(end-start) / 1e6
				}
				if span.Status.Code == 2 {
					item.Errors = 1
				}
				observations = append(observations, item)
				if len(observations) > maxObservations {
					return nil, nil, errors.New("otlp export too large")
				}
			}
		}
	}
	var coverage []Coverage
	for covered, seen := range keys {
		item := Coverage{Source: "otlp", Environment: covered.environment, Unit: covered.service, AsOf: now, TTL: 15 * 60_000}
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
