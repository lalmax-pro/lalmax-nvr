package observability

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type Provider struct {
	traces  *sdktrace.TracerProvider
	metrics *metric.MeterProvider
}

type ProviderConfig struct {
	Enabled               bool
	Endpoint              string
	ServiceName           string
	TracesEnabled         bool
	MetricsEnabled        bool
	Headers               map[string]string
	Timeout               time.Duration
	MetricsExportInterval time.Duration
}

func NewProvider(ctx context.Context, version string, cfg ProviderConfig) (*Provider, error) {
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", cfg.ServiceName),
		attribute.String("service.version", version),
	))
	if err != nil {
		return nil, err
	}

	traceOptions := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if cfg.Enabled && cfg.TracesEnabled {
		exporter, exportErr := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpointURL(signalEndpoint(cfg.Endpoint, "traces")),
			otlptracehttp.WithHeaders(cloneHeaders(cfg.Headers)),
			otlptracehttp.WithTimeout(cfg.Timeout),
		)
		if exportErr != nil {
			return nil, fmt.Errorf("create OTLP trace exporter: %w", exportErr)
		}
		traceOptions = append(traceOptions, sdktrace.WithBatcher(exporter))
	}
	tracerProvider := sdktrace.NewTracerProvider(traceOptions...)

	manualReader := metric.NewManualReader()
	metricOptions := []metric.Option{metric.WithResource(res), metric.WithReader(manualReader)}
	if cfg.Enabled && cfg.MetricsEnabled {
		exporter, exportErr := otlpmetrichttp.New(ctx,
			otlpmetrichttp.WithEndpointURL(signalEndpoint(cfg.Endpoint, "metrics")),
			otlpmetrichttp.WithHeaders(cloneHeaders(cfg.Headers)),
			otlpmetrichttp.WithTimeout(cfg.Timeout),
		)
		if exportErr != nil {
			_ = tracerProvider.Shutdown(ctx)
			return nil, fmt.Errorf("create OTLP metric exporter: %w", exportErr)
		}
		metricOptions = append(metricOptions, metric.WithReader(metric.NewPeriodicReader(exporter, metric.WithInterval(cfg.MetricsExportInterval))))
	}
	meterProvider := metric.NewMeterProvider(metricOptions...)

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return &Provider{traces: tracerProvider, metrics: meterProvider}, nil
}

func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return errors.Join(p.metrics.Shutdown(ctx), p.traces.Shutdown(ctx))
}

func signalEndpoint(base, signal string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	u.Path = path.Join("/", u.Path, "v1", signal)
	return u.String()
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(headers))
	for name, value := range headers {
		cloned[name] = value
	}
	return cloned
}
