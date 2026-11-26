package ledger

import (
	"context"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// Exporter sends one OTLP log record per ended run, on the same OTLP/HTTP log path the audit
// export uses. Each record carries the run's count (always 1), duration and cost as attributes, so
// a collector can sum them. No new OpenTelemetry module is needed.
type Exporter struct{ p *sdklog.LoggerProvider }

// NewExporter sends through exp. Tests pass an in-memory exporter.
func NewExporter(exp sdklog.Exporter) *Exporter {
	return &Exporter{p: sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))}
}

// NewExporterFromEnv returns an exporter when OTEL_EXPORTER_OTLP_ENDPOINT or
// OTEL_EXPORTER_OTLP_LOGS_ENDPOINT is set, and nil otherwise.
func NewExporterFromEnv(ctx context.Context) (*Exporter, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT") == "" {
		return nil, nil
	}
	exp, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, err
	}
	return &Exporter{p: sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)))}, nil
}

// Export queues the metrics of e. Model names and token counts stay in the ledger file and the
// analytics API; the export carries only counts, durations and cost.
func (x *Exporter) Export(ctx context.Context, e Entry) {
	var rec otellog.Record
	at := e.StartedAt
	if e.EndedAt != nil {
		at = *e.EndedAt
	}
	rec.SetTimestamp(at)
	rec.SetObservedTimestamp(time.Now())
	rec.SetSeverity(otellog.SeverityInfo)
	rec.SetBody(attribute.StringValue("kaas.run.ended"))
	rec.AddAttributes(
		attribute.String("kaas.run.id", e.RunID),
		attribute.String("kaas.run.repo", e.Repo),
		attribute.String("kaas.run.team", e.Team),
		attribute.String("kaas.run.harness", e.Agent.Harness),
		attribute.String("kaas.run.status", e.Status),
		attribute.Int("kaas.run.count", 1),
	)
	if d, ok := Duration(e); ok {
		rec.AddAttributes(attribute.Int64("kaas.run.duration_ms", d.Milliseconds()))
	}
	if e.CostUSD != nil {
		rec.AddAttributes(attribute.Float64("kaas.run.cost_usd", *e.CostUSD))
	}
	x.p.Logger("kaas-factory/ledger").Emit(ctx, rec)
}

// Close flushes queued records and stops the exporter.
func (x *Exporter) Close(ctx context.Context) error { return x.p.Shutdown(ctx) }
