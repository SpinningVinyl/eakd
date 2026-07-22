//go:build linux

package action

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
)

type client struct {
	connection *net.UnixConn
	queue      chan []byte
}

// Server broadcasts newline-delimited action objects. It authenticates each
// connection with SO_PEERCRED rather than trusting data supplied by a client.
type Server struct {
	path    string
	allowed map[uint32]bool
	logger  *log.Logger
	ready   chan struct{}

	mu      sync.Mutex
	clients map[*client]struct{}
}

func NewServer(path string, allowedUIDs []uint32, logger *log.Logger) *Server {
	allowed := make(map[uint32]bool, len(allowedUIDs))
	for _, uid := range allowedUIDs {
		allowed[uid] = true
	}
	return &Server{
		path: path, allowed: allowed, logger: logger, ready: make(chan struct{}),
		clients: make(map[*client]struct{}),
	}
}

// Ready is closed only after the listening socket has been created and its
// final permissions have been applied successfully.
func (s *Server) Ready() <-chan struct{} { return s.ready }

func (s *Server) Serve(ctx context.Context) error {
	if len(s.allowed) == 0 {
		return errors.New("allowed_uids must contain at least one eakc user")
	}
	_ = os.Remove(s.path)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.path, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.path, err)
	}
	defer func() {
		listener.Close()
		_ = os.Remove(s.path)
		s.closeClients()
	}()
	if err := os.Chmod(s.path, 0o666); err != nil {
		return fmt.Errorf("chmod action socket: %w", err)
	}
	close(s.ready)
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept action client: %w", acceptErr)
		}
		uid, credErr := peerUID(connection)
		if credErr != nil || !s.allowed[uid] {
			if credErr != nil {
				s.logger.Printf("reject action client: %v", credErr)
			} else {
				s.logger.Printf("reject action client uid %d", uid)
			}
			_ = connection.Close()
			continue
		}
		c := &client{connection: connection, queue: make(chan []byte, 16)}
		s.mu.Lock()
		s.clients[c] = struct{}{}
		s.mu.Unlock()
		s.logger.Printf("accepted action client uid %d", uid)
		go s.writeClient(c)
	}
}

// Publish never blocks keyboard forwarding. A client that cannot consume 16
// queued actions is disconnected instead of stalling the trusted input path.
func (s *Server) Publish(action string) {
	data, _ := json.Marshal(Notification{Action: action})
	data = append(data, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		copyOfData := append([]byte(nil), data...)
		select {
		case c.queue <- copyOfData:
		default:
			delete(s.clients, c)
			close(c.queue)
			_ = c.connection.Close()
		}
	}
}

func (s *Server) writeClient(c *client) {
	for data := range c.queue {
		if _, err := c.connection.Write(data); err != nil {
			break
		}
	}
	s.mu.Lock()
	if _, exists := s.clients[c]; exists {
		delete(s.clients, c)
		close(c.queue)
	}
	s.mu.Unlock()
	_ = c.connection.Close()
}

func (s *Server) closeClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		delete(s.clients, c)
		close(c.queue)
		_ = c.connection.Close()
	}
}

func peerUID(connection *net.UnixConn) (uint32, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		credential, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if err != nil {
			controlErr = err
			return
		}
		uid = credential.Uid
	}); err != nil {
		return 0, err
	}
	return uid, controlErr
}
