package opgraph

import (
	"fmt"
	"testing"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

func text(key, value string) *commonv1.KeyValue {
	return &commonv1.KeyValue{Key: key, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value}}}
}

// Only counts and cost come out of an agent export, and only for a resource
// that names a run: Claude Code and Codex names, either temporality.
func TestAgentExportsDecodeToRunCountsOnly(t *testing.T) {
	run := []*commonv1.KeyValue{text(RunAttribute, "r1")}
	sum := func(name, unit string, temporality metricsv1.AggregationTemporality, value int64, attributes ...*commonv1.KeyValue) *metricsv1.Metric {
		return &metricsv1.Metric{Name: name, Unit: unit, Data: &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{AggregationTemporality: temporality,
			DataPoints: []*metricsv1.NumberDataPoint{{Attributes: attributes, Value: &metricsv1.NumberDataPoint_AsInt{AsInt: value}}}}}}
	}
	histogram := func(name string, value float64, attributes ...*commonv1.KeyValue) *metricsv1.Metric {
		return &metricsv1.Metric{Name: name, Data: &metricsv1.Metric_Histogram{Histogram: &metricsv1.Histogram{AggregationTemporality: metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
			DataPoints: []*metricsv1.HistogramDataPoint{{Attributes: attributes, StartTimeUnixNano: 7, Sum: &value}}}}}
	}
	delta := metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
	metrics, err := proto.Marshal(&metricsv1.MetricsData{ResourceMetrics: []*metricsv1.ResourceMetrics{
		{Resource: &resourcev1.Resource{Attributes: run}, ScopeMetrics: []*metricsv1.ScopeMetrics{{Metrics: []*metricsv1.Metric{
			sum("claude_code.token.usage", "tokens", delta, 100, text("type", "input")),
			sum("claude_code.token.usage", "tokens", delta, 20, text("type", "output")),
			sum("claude_code.token.usage", "tokens", delta, 900, text("type", "cacheRead")),
			{Name: "claude_code.cost.usage", Unit: "USD", Data: &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{AggregationTemporality: delta,
				DataPoints: []*metricsv1.NumberDataPoint{{Value: &metricsv1.NumberDataPoint_AsDouble{AsDouble: 0.25}}}}}},
			histogram("codex.turn.token_usage", 50, text("token_type", "input")),
			histogram("codex.turn.cost_microusd", 500_000),
			sum("claude_code.lines_of_code.count", "", delta, 9),
			sum("claude_code.cost.usage", "EUR", delta, 9),
		}}}},
		{Resource: &resourcev1.Resource{}, ScopeMetrics: []*metricsv1.ScopeMetrics{{Metrics: []*metricsv1.Metric{sum("claude_code.token.usage", "tokens", delta, 1, text("type", "input"))}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	points, err := DecodeAgentMetrics(metrics, true)
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, point := range points {
		got += fmt.Sprintf("%s:%s:%g:%v ", point.Run, point.Kind, point.Value, point.Series != "")
	}
	if want := "r1:tokens_in:100:false r1:tokens_out:20:false r1:cost_usd:0.25:false r1:tokens_in:50:true r1:cost_usd:0.5:true "; got != want {
		t.Fatalf("metrics decoded to %q, want %q", got, want)
	}

	// Claude Code names the event in event.name, Codex prefixes it, and an
	// SDK may use the record's own event name; nothing else is read.
	logs, err := proto.Marshal(&logsv1.LogsData{ResourceLogs: []*logsv1.ResourceLogs{
		{Resource: &resourcev1.Resource{Attributes: run}, ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: []*logsv1.LogRecord{
			{Attributes: []*commonv1.KeyValue{text("event.name", "tool_result"), text("tool_input", "secret")}},
			{Attributes: []*commonv1.KeyValue{text("event.name", "codex.tool_result")}},
			{EventName: "gen_ai.api_request"},
			{Attributes: []*commonv1.KeyValue{text("event.name", "codex.tool_result_ready")}},
			{Attributes: []*commonv1.KeyValue{text("event.name", "user_prompt"), text("prompt", "secret")}},
		}}}},
		{Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{text("service.name", "claude-code")}}, ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: []*logsv1.LogRecord{
			{Attributes: []*commonv1.KeyValue{text("event.name", "tool_result")}},
		}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	points, err = DecodeAgentLogs(logs, true)
	if err != nil {
		t.Fatal(err)
	}
	got = fmt.Sprint(points)
	if want := "[{r1 tool_call 1 } {r1 tool_call 1 } {r1 api_request 1 }]"; got != want {
		t.Fatalf("logs decoded to %s, want %s", got, want)
	}
	// The OTLP JSON mapping decodes the same way.
	points, err = DecodeAgentLogs([]byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"dark_factory.run.id","value":{"stringValue":"r1"}}]},"scopeLogs":[{"logRecords":[{"traceId":"5b8efff798038103d269b633813fc60c","attributes":[{"key":"event.name","value":{"stringValue":"api_request"}}]}]}]}]}`), false)
	if err != nil || fmt.Sprint(points) != "[{r1 api_request 1 }]" {
		t.Fatalf("JSON logs decoded to %v, %v", points, err)
	}
	if _, err := DecodeAgentMetrics([]byte("not protobuf"), true); err == nil {
		t.Fatal("malformed protobuf decoded")
	}
}
