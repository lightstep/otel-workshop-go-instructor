module fib

go 1.16

require (
	go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace v0.26.0
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.26.0
	go.opentelemetry.io/otel v1.1.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.1.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.1.0
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.1.0
	go.opentelemetry.io/otel/metric v0.24.0
	go.opentelemetry.io/otel/sdk v1.1.0
	go.opentelemetry.io/otel/trace v1.1.0
	google.golang.org/grpc v1.41.0
)
