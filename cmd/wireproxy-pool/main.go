package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yiivgeny/wireproxy-pool/internal/app"
)

func main() {
	logger := log.New(os.Stderr, "wireproxy-pool: ", log.LstdFlags)
	config, inputs := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, config, inputs, logger); err != nil {
		logger.Printf("error: %v", err)
		os.Exit(1)
	}
}

func parseFlags() (app.Config, []string) {
	var config app.Config
	flag.StringVar(&config.LogLevel, "log-level", "info", "log level: info or debug")
	flag.StringVar(&config.SOCKSListen, "socks-listen", "127.0.0.1:1080", "SOCKS5 TCP listen address")
	flag.StringVar(&config.MetricsListen, "metrics-listen", "127.0.0.1:9090", "Prometheus HTTP listen address")
	flag.StringVar(&config.HealthTarget, "health-target", "one.one.one.one:443", "TCP health target as host:port")
	flag.DurationVar(&config.HealthInterval, "health-interval", 15*time.Second, "interval between backend health checks")
	flag.DurationVar(&config.HealthTimeout, "health-timeout", 5*time.Second, "timeout for each backend health check")
	flag.DurationVar(&config.ConnectTimeout, "connect-timeout", 10*time.Second, "timeout for each backend request attempt")
	flag.IntVar(&config.FailureThreshold, "failure-threshold", 3, "consecutive failed checks before marking an available backend unavailable")
	flag.IntVar(&config.SuccessThreshold, "success-threshold", 2, "consecutive successful checks required to recover an unavailable backend")
	flag.DurationVar(&config.ShutdownTimeout, "shutdown-timeout", 5*time.Second, "grace period for HTTP shutdown")
	flag.Usage = func() {
		output := flag.CommandLine.Output()
		fmt.Fprintln(output, "Usage: wireproxy-pool [flags] CONFIG_OR_GLOB [CONFIG_OR_GLOB ...]")
		fmt.Fprintln(output, "Quoted glob patterns are expanded by wireproxy-pool itself.")
		fmt.Fprintln(output)
		flag.PrintDefaults()
	}
	flag.Parse()
	return config, flag.Args()
}
