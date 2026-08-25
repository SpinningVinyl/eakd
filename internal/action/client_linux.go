// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package action

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"
)

const (
	initialReconnectDelay = 100 * time.Millisecond
	maximumReconnectDelay = 5 * time.Second
	maximumMessageSize    = 64 * 1024
)

// Client maintains a connection to eakd and emits opaque action IDs. Socket
// creation and daemon restarts are normal, so connection failures are retried
// until the context is cancelled.
type Client struct {
	path   string
	logger *log.Logger
	dial   func(context.Context, string, string) (net.Conn, error)
}

func NewClient(path string, logger *log.Logger) *Client {
	return &Client{
		path:   path,
		logger: logger,
		dial:   (&net.Dialer{}).DialContext,
	}
}

func (c *Client) Run(ctx context.Context, actions chan<- string) error {
	delay := initialReconnectDelay
	for {
		connection, err := c.dial(ctx, "unix", c.path)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.logger.Printf("connect to %s: %v; retrying", c.path, err)
			if !waitForRetry(ctx, delay) {
				return nil
			}
			delay = min(delay*2, maximumReconnectDelay)
			continue
		}

		c.logger.Printf("connected to %s", c.path)
		delay = initialReconnectDelay
		stopClose := context.AfterFunc(ctx, func() { _ = connection.Close() })
		err = readNotifications(ctx, connection, actions)
		stopClose()
		_ = connection.Close()
		if ctx.Err() != nil {
			return nil
		}
		c.logger.Printf("connection to %s lost: %v; reconnecting", c.path, err)
		if !waitForRetry(ctx, delay) {
			return nil
		}
	}
}

func readNotifications(ctx context.Context, connection net.Conn, actions chan<- string) error {
	scanner := bufio.NewScanner(connection)
	scanner.Buffer(make([]byte, 4096), maximumMessageSize)
	for scanner.Scan() {
		var notification Notification
		if err := json.Unmarshal(scanner.Bytes(), &notification); err != nil {
			return fmt.Errorf("decode action notification: %w", err)
		}
		if notification.Action == "" {
			return fmt.Errorf("received empty action ID")
		}
		select {
		case actions <- notification.Action:
		case <-ctx.Done():
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read action notification: %w", err)
	}
	return fmt.Errorf("action socket closed")
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
