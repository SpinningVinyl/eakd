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
	uid        uint32
	connection clientConnection
	queue      chan []byte
}

type clientConnection interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Close() error
}

// Server broadcasts newline-delimited action objects. It authenticates each
// connection with SO_PEERCRED rather than trusting data supplied by a client.
type Server struct {
	path    string
	allowed map[uint32]bool
	logger  *log.Logger
	ready   chan struct{}

	mu      sync.Mutex
	clients map[uint32]*client
}

func NewServer(path string, allowedUIDs []uint32, logger *log.Logger) *Server {
	allowed := make(map[uint32]bool, len(allowedUIDs))
	for _, uid := range allowedUIDs {
		allowed[uid] = true
	}
	return &Server{
		path: path, allowed: allowed, logger: logger, ready: make(chan struct{}),
		clients: make(map[uint32]*client),
	}
}

// Ready is closed only after the listening socket has been created and its
// final permissions have been applied successfully.
func (s *Server) Ready() <-chan struct{} { return s.ready }

func (s *Server) Serve(ctx context.Context) error {
	if len(s.allowed) == 0 {
		return errors.New("allowed_uids must contain at least one eakc user")
	}
	if err := removeSocket(s.path); err != nil {
		return err
	}
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
		c := &client{uid: uid, connection: connection, queue: make(chan []byte, 16)}
		if !s.addClient(c) {
			s.logger.Printf("reject duplicate action client uid %d", uid)
			_ = connection.Close()
			continue
		}
		s.logger.Printf("accepted action client uid %d", uid)
		go s.writeClient(c)
		go s.watchClient(c)
	}
}

func (s *Server) addClient(c *client) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.clients[c.uid]; exists {
		return false
	}
	s.clients[c.uid] = c
	return true
}

// Publish never blocks keyboard forwarding. A client that cannot consume 16
// queued actions is disconnected instead of stalling the trusted input path.
func (s *Server) Publish(action string) {
	data, _ := json.Marshal(Notification{Action: action})
	data = append(data, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	for uid, c := range s.clients {
		select {
		case c.queue <- data:
		default:
			delete(s.clients, uid)
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
	s.removeClient(c)
}

// watchClient releases the UID's connection slot promptly when the peer
// disconnects. The action protocol is one-way, so any received data also
// terminates the connection.
func (s *Server) watchClient(c *client) {
	var buffer [1]byte
	_, _ = c.connection.Read(buffer[:])
	s.removeClient(c)
}

func (s *Server) removeClient(c *client) {
	s.mu.Lock()
	if current, exists := s.clients[c.uid]; exists && current == c {
		delete(s.clients, c.uid)
		close(c.queue)
		_ = c.connection.Close()
	}
	s.mu.Unlock()
}

func (s *Server) closeClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for uid, c := range s.clients {
		delete(s.clients, uid)
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

func removeSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect socket path %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to remove non-socket entry at %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("error removing stale socket: %w", err)
	}
	return nil
}
