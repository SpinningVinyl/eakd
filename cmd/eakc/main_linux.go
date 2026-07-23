//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"eak/internal/action"
	"eak/internal/buildinfo"
	"eak/internal/clientconfig"
	"eak/internal/executor"
)

func main() {
	logger := log.New(os.Stderr, "eakc: ", log.LstdFlags|log.Lmicroseconds)
	configPath := flag.String("config", "", "path to the user action configuration (default: user config directory)")
	checkOnly := flag.Bool("check", false, "validate configuration and exit")
	allowInsecure := flag.Bool("allow-insecure-config", false, "allow an insecure configuration file (development only)")
	showVersion := flag.Bool("version", false, "display version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("eakc %s\n", buildinfo.Version)
		return
	}
	if *configPath == "" {
		defaultConfig, err := clientconfig.DefaultPath()
		if err != nil {
			logger.Fatal(err)
		}
		*configPath = defaultConfig
	}

	cfg, err := clientconfig.Load(*configPath, *allowInsecure)
	if err != nil {
		logger.Fatal(err)
	}
	if *checkOnly {
		fmt.Printf("configuration %s is valid\n", *configPath)
		return
	}

	if err := run(cfg, logger); err != nil {
		logger.Fatal(err)
	}
}

func run(cfg clientconfig.Config, logger *log.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	actions := make(chan string, cfg.QueueSize)
	clientErrors := make(chan error, 1)
	client := action.NewClient(cfg.SocketPath, logger)
	go func() {
		clientErrors <- client.Run(ctx, actions)
		close(actions)
	}()

	runner := executor.New(cfg.Actions, cfg.MaxParallel, logger)
	runner.Run(ctx, actions)
	stop()
	if err := <-clientErrors; err != nil {
		return fmt.Errorf("action client: %w", err)
	}
	return nil
}
