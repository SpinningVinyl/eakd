// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"eak/internal/action"
	"eak/internal/buildinfo"
	"eak/internal/config"
	"eak/internal/engine"
	"eak/internal/input"
	"eak/internal/linuxinput"
	"eak/internal/systemd"
)

const virtualKeyboardName = "eakd virtual keyboard"

func main() {
	configPath := flag.String("config", "/etc/eak/eakd.json", "path to the root-owned configuration file")
	checkOnly := flag.Bool("check", false, "validate configuration and exit")
	allowInsecure := flag.Bool("allow-insecure-config", false, "allow non-root/writable configuration (development only)")
	showVersion := flag.Bool("version", false, "display version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("eakd %s\n", buildinfo.Version)
		return
	}

	logger := log.New(os.Stderr, "eakd: ", log.LstdFlags|log.Lmicroseconds)
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		logger.Fatalf("unsupported architecture %s: ioctl encoding has only been audited on amd64 and arm64", runtime.GOARCH)
	}
	cfg, err := config.Load(*configPath, *allowInsecure)
	if err != nil {
		logger.Fatal(err)
	}
	if *checkOnly {
		fmt.Printf("configuration %s is valid\n", *configPath)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, logger); err != nil {
		logger.Fatal(err)
	}
}

func run(parent context.Context, cfg config.Config, logger *log.Logger) error {
	ctx, cancel := context.WithCancel(parent)

	server := action.NewServer(cfg.SocketPath, cfg.AllowedUIDs, logger)
	serverReady := server.Ready()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve(ctx) }()

	virtual, err := linuxinput.CreateVirtualKeyboard(virtualKeyboardName)
	if err != nil {
		cancel()
		return err
	}
	defer virtual.Close()
	manager := linuxinput.NewManager(virtual, logger)

	processor := engine.New(cfg)
	forwarder := linuxinput.NewForwarder(virtual)
	messages := make(chan input.Message, 64)
	managerDone := make(chan struct{})
	go func() {
		manager.Run(ctx, messages)
		close(managerDone)
	}()
	managerReady := manager.Ready()
	notified := false
	// This defer was registered after virtual.Close, so it runs first. No
	// virtual device is destroyed until every physical EVIOCGRAB is released.
	defer func() {
		cancel()
		<-managerDone
	}()

	var timer *time.Timer
	var timerChannel <-chan time.Time
	resetTimer := func() {
		deadline, exists := processor.Deadline()
		if !exists {
			if timer != nil {
				timer.Stop()
			}
			timerChannel = nil
			return
		}
		delay := time.Until(deadline)
		if delay < 0 {
			delay = 0
		}
		if timer == nil {
			timer = time.NewTimer(delay)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(delay)
		}
		timerChannel = timer.C
	}

	apply := func(result engine.Result) error {
		for _, output := range result.Output {
			switch output.Kind {
			case engine.ForwardFrame:
				if err := forwarder.Frame(output.Frame); err != nil {
					return err
				}
			case engine.ReconcileDevice:
				if err := forwarder.Resync(output.Device, output.Pressed); err != nil {
					return err
				}
			case engine.EmitAction:
				logger.Printf("action %s", output.Action)
				server.Publish(output.Action)
			}
		}
		resetTimer()
		return nil
	}
	notifyReady := func() error {
		if notified || serverReady != nil || managerReady != nil {
			return nil
		}
		if err := systemd.Ready(); err != nil {
			return err
		}
		notified = true
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return apply(processor.Close())
		case <-managerReady:
			managerReady = nil
			if err := notifyReady(); err != nil {
				return err
			}
		case <-serverReady:
			serverReady = nil
			if err := notifyReady(); err != nil {
				return err
			}
		case err := <-serverErrors:
			if err != nil {
				return err
			}
			return apply(processor.Close())
		case now := <-timerChannel:
			if err := apply(processor.HandleTimeout(now)); err != nil {
				return fmt.Errorf("forward timeout buffer: %w", err)
			}
		case message, ok := <-messages:
			if !ok {
				if ctx.Err() != nil {
					return apply(processor.Close())
				}
				return fmt.Errorf("input manager stopped unexpectedly")
			}
			if message.Err != nil {
				return message.Err
			}
			if message.Frame != nil {
				if err := apply(processor.HandleFrame(*message.Frame, time.Now())); err != nil {
					return fmt.Errorf("forward input: %w", err)
				}
			}
			if message.Resync != nil {
				if err := apply(processor.Reconcile(message.Resync.Device, message.Resync.Pressed)); err != nil {
					return fmt.Errorf("resynchronize %s: %w", message.Resync.Device, err)
				}
			}
			if message.Removed != "" {
				if err := apply(processor.Reconcile(message.Removed, nil)); err != nil {
					return fmt.Errorf("release removed device %s: %w", message.Removed, err)
				}
			}
		}
	}
}
