package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	// "go.opentelemetry.io/otel/exporters/otlp/otlpmetric"
	"go.opentelemetry.io/otel/metric"
	mglobal "go.opentelemetry.io/otel/metric/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.7.0"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/encoding/gzip"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
)

func newResource() *resource.Resource {
	r, _ := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String("fib"),
			semconv.ServiceVersionKey.String("v0.1.0"),
			attribute.String("environment", "workshop"),
		),
	)
	return r
}

func main() {
	// serviceName, _ := os.LookupEnv("PROJECT_NAME")
	ls_access_token, _ := os.LookupEnv("LS_ACCESS_TOKEN")
	ctx := context.Background()

	// stdout exporter
	std, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		log.Fatal(err)
	}

	//OTLP exporter
	exporter, err := otlptrace.New(ctx, otlptracegrpc.NewClient(
		otlptracegrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, "")),
		otlptracegrpc.WithEndpoint("ingest.lightstep.com:443"),
		otlptracegrpc.WithHeaders(map[string]string{"lightstep-access-token":ls_access_token}),
		otlptracegrpc.WithCompressor(gzip.Name),),
	)
	if err != nil {
		log.Fatalf("Could not start web server: %s", err)
	}
	
	// Define TracerProvider
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	    sdktrace.WithBatcher(std),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(newResource()),
	)

	// Set TracerProvider
	otel.SetTracerProvider(tracerProvider)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	//Establish metrics exporter
	// metricClient := otlpMetric.client()
	// otlpmetricExp, err := otlpmetric.New(ctx, metricClient)

	// // Set Prometheus metrics
	// prom, err := prometheus.InstallNewPipeline(prometheus.Config{
	// 	DefaultHistogramBoundaries: []float64{0.5, 0.9, 0.99},
	// })

	mux := http.NewServeMux()
	mux.Handle("/", otelhttp.NewHandler(otelhttp.WithRouteTag("/", http.HandlerFunc(rootHandler)), "root", otelhttp.WithPublicEndpoint()))
	mux.Handle("/favicon.ico", http.NotFoundHandler())
	mux.Handle("/fib", otelhttp.NewHandler(otelhttp.WithRouteTag("/fib", http.HandlerFunc(fibHandler)), "fibonacci", otelhttp.WithPublicEndpoint()))
	mux.Handle("/fibinternal", otelhttp.NewHandler(otelhttp.WithRouteTag("/fibinternal", http.HandlerFunc(fibHandler)), "fibonacci"))
	// mux.Handle("/metrics", prom)
	os.Stderr.WriteString("Initializing the server...\n")

	go updateDiskMetrics(context.Background())

	err = http.ListenAndServe("127.0.0.1:3000", mux)
	if err != nil {
		log.Fatalf("Could not start web server: %s", err)
	}
}

func rootHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	trace.SpanFromContext(ctx).AddEvent("annotation within span")
	_ = dbHandler(ctx, "foo")

	fmt.Fprintf(w, "Your server is live! Try to navigate to: http://localhost:3000/fib?i=6")
}

func fibHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	tr := otel.Tracer("fibHandler")
	var err error
	var i int
	if len(req.URL.Query()["i"]) != 1 {
		err = fmt.Errorf("Wrong number of arguments.")
	} else {
		i, err = strconv.Atoi(req.URL.Query()["i"][0])
	}
	if err != nil {
		fmt.Fprintf(w, "Couldn't parse index '%s'.", req.URL.Query()["i"])
		w.WriteHeader(503)
		return
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.Int("parameter", i))
	ret := 0
	failed := false

	if i < 2 {
		ret = 1
	} else {
		// Call /fib?i=(n-1) and /fib?i=(n-2) and add them together.
		var mtx sync.Mutex
		var wg sync.WaitGroup
		client := http.DefaultClient
		for offset := 1; offset < 3; offset++ {
			wg.Add(1)
			go func(n int) {
				err := func() error {
					ictx, sp := tr.Start(ctx, "fibClient")
					defer sp.End()
					url := fmt.Sprintf("http://127.0.0.1:3000/fibinternal?i=%d", n)
					trace.SpanFromContext(ictx).SetAttributes(attribute.String("url", url))
					trace.SpanFromContext(ictx).AddEvent("Fib loop count", trace.WithAttributes(attribute.Int("fib-loop", n)))
					req, _ := http.NewRequestWithContext(ictx, "GET", url, nil)
					ictx, req = otelhttptrace.W3C(ictx, req)
					otelhttptrace.Inject(ictx, req)
					res, err := client.Do(req)
					if err != nil {
						return err
					}
					body, err := ioutil.ReadAll(res.Body)
					res.Body.Close()
					if err != nil {
						return err
					}
					resp, err := strconv.Atoi(string(body))
					if err != nil {
						trace.SpanFromContext(ictx).SetStatus(codes.Error, "failure parsing")
						return err
					}
					trace.SpanFromContext(ictx).SetAttributes(attribute.Int("result", resp))
					mtx.Lock()
					defer mtx.Unlock()
					ret += resp
					return err
				}()
				if err != nil {
					if !failed {
						w.WriteHeader(503)
						failed = true
					}
					fmt.Fprintf(w, "Failed to call child index '%s'.\n", n)
				}
				wg.Done()
			}(i - offset)
		}
		wg.Wait()
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.Int("result", ret))
	fmt.Fprintf(w, "%d", ret)
}

func updateDiskMetrics(ctx context.Context) {
	appKey := attribute.Key("glitch.com/app")                // The Glitch app name.
	containerKey := attribute.Key("glitch.com/container_id") // The Glitch container id.

	meter := mglobal.Meter("container")
	mem, _ := meter.NewInt64Counter("mem_usage",
		metric.WithDescription("Amount of memory used."),
	)
	used, _ := meter.NewFloat64Counter("disk_usage",
		metric.WithDescription("Amount of disk used."),
	)
	quota, _ := meter.NewFloat64Counter("disk_quota",
		metric.WithDescription("Amount of disk quota available."),
	)
	goroutines, _ := meter.NewInt64Counter("num_goroutines",
		metric.WithDescription("Amount of goroutines running."),
	)

	var m runtime.MemStats
	for {
		runtime.ReadMemStats(&m)

		var stat syscall.Statfs_t
		wd, _ := os.Getwd()
		syscall.Statfs(wd, &stat)

		all := float64(stat.Blocks) * float64(stat.Bsize)
		free := float64(stat.Bfree) * float64(stat.Bsize)

		meter.RecordBatch(ctx, []attribute.KeyValue{
			appKey.String(os.Getenv("PROJECT_DOMAIN")),
			containerKey.String(os.Getenv("HOSTNAME"))},
			used.Measurement(all-free),
			quota.Measurement(all),
			mem.Measurement(int64(m.Sys)),
			goroutines.Measurement(int64(runtime.NumGoroutine())),
		)
		time.Sleep(time.Minute)
	}
}

func dbHandler(ctx context.Context, color string) int {
	tr := otel.Tracer("dbHandler")
	ctx, span := tr.Start(ctx, "database")
	defer span.End()

	// Pretend we talked to a database here.
	return 0
}
