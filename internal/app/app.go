package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/yiivgeny/wireproxy-pool/internal/configpaths"
	"github.com/yiivgeny/wireproxy-pool/internal/health"
	poolmetrics "github.com/yiivgeny/wireproxy-pool/internal/metrics"
	"github.com/yiivgeny/wireproxy-pool/internal/pool"
	"github.com/yiivgeny/wireproxy-pool/internal/proxy"
	"github.com/yiivgeny/wireproxy-pool/internal/wgbackend"
)

type Config struct {
	LogLevel         string
	SOCKSListen      string
	MetricsListen    string
	HealthTarget     string
	HealthInterval   time.Duration
	HealthTimeout    time.Duration
	ConnectTimeout   time.Duration
	FailureThreshold int
	SuccessThreshold int
	ShutdownTimeout  time.Duration
}

type serviceError struct {
	name string
	err  error
}

func Run(ctx context.Context, config Config, inputs []string, logger *log.Logger) (runErr error) {
	if logger == nil {
		logger = log.Default()
	}
	paths, err := configpaths.Expand(inputs)
	if err != nil {
		return err
	}
	target, err := health.ParseTarget(config.HealthTarget)
	if err != nil {
		return err
	}
	if config.HealthInterval <= 0 {
		return fmt.Errorf("health interval must be positive")
	}
	if config.HealthTimeout <= 0 {
		return fmt.Errorf("health timeout must be positive")
	}
	if config.ConnectTimeout <= 0 {
		return fmt.Errorf("connect timeout must be positive")
	}
	if config.FailureThreshold < 1 {
		return fmt.Errorf("failure threshold must be at least 1")
	}
	if config.SuccessThreshold < 1 {
		return fmt.Errorf("success threshold must be at least 1")
	}
	if config.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown timeout must be positive")
	}
	logLevel := strings.ToLower(config.LogLevel)
	if logLevel == "" {
		logLevel = "info"
	}
	if logLevel != "info" && logLevel != "debug" {
		return fmt.Errorf("log level must be info or debug")
	}

	socksListener, err := net.Listen("tcp", config.SOCKSListen)
	if err != nil {
		return fmt.Errorf("listen for SOCKS5 on %s: %w", config.SOCKSListen, err)
	}
	defer func() {
		if err := socksListener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			runErr = errors.Join(runErr, fmt.Errorf("close SOCKS listener: %w", err))
		}
	}()

	metricsListener, err := net.Listen("tcp", config.MetricsListen)
	if err != nil {
		return fmt.Errorf("listen for metrics on %s: %w", config.MetricsListen, err)
	}
	defer func() {
		if err := metricsListener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			runErr = errors.Join(runErr, fmt.Errorf("close metrics listener: %w", err))
		}
	}()

	backends := make([]pool.Backend, 0, len(paths))
	defer func() {
		if err := pool.CloseAll(backends); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	for _, configPath := range paths {
		id := configpaths.BackendID(configPath)
		backend, err := wgbackend.Start(configPath, id)
		if err != nil {
			return fmt.Errorf("start backend %s (%s): %w", id, configPath, err)
		}
		backends = append(backends, backend)
		logger.Printf("started backend %s from %s", id, configPath)
	}

	backendPool, err := pool.New(backends, config.ConnectTimeout, config.FailureThreshold, config.SuccessThreshold)
	if err != nil {
		return err
	}
	registry := prometheus.NewRegistry()
	metrics, err := poolmetrics.New(registry, backendPool)
	if err != nil {
		return err
	}
	backendPool.SetObserver(metrics)

	monitor, err := health.NewMonitor(
		backendPool,
		health.TCPProber{},
		target,
		config.HealthInterval,
		config.HealthTimeout,
		logger,
	)
	if err != nil {
		return err
	}
	socksServer, err := proxy.NewSOCKSServer(backendPool, logger, logLevel == "debug")
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	monitor.CheckAll(runCtx)
	if err := runCtx.Err(); err != nil {
		return nil
	}
	monitor.Start(runCtx)

	trackingListener := proxy.NewTrackingListener(socksListener)
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	httpServer := &http.Server{
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errorsCh := make(chan serviceError, 2)
	var servers sync.WaitGroup
	servers.Add(2)
	go func() {
		defer servers.Done()
		err := socksServer.Serve(trackingListener)
		if err != nil && !errors.Is(err, net.ErrClosed) {
			errorsCh <- serviceError{name: "SOCKS5", err: err}
		}
	}()
	go func() {
		defer servers.Done()
		err := httpServer.Serve(metricsListener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			errorsCh <- serviceError{name: "metrics", err: err}
		}
	}()

	logger.Printf("SOCKS5 listening on %s", socksListener.Addr())
	logger.Printf("metrics listening on http://%s/metrics", metricsListener.Addr())
	logger.Printf("health target %s, interval %s, timeout %s", target, config.HealthInterval, config.HealthTimeout)

	var servingErr error
	select {
	case <-ctx.Done():
	case failure := <-errorsCh:
		servingErr = fmt.Errorf("%s server failed: %w", failure.name, failure.err)
	}

	cancel()
	if err := trackingListener.Shutdown(); err != nil {
		servingErr = errors.Join(servingErr, fmt.Errorf("shut down SOCKS5: %w", err))
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		servingErr = errors.Join(servingErr, fmt.Errorf("shut down metrics: %w", err))
	}
	shutdownCancel()
	servers.Wait()
	monitor.Wait()

	return servingErr
}
