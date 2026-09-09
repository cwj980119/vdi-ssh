package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

type Server struct {
	cfg      Config
	prefixes []netip.Prefix
	keyPath  string
	signer   ssh.Signer
	log      *slog.Logger
}

func (s *Server) allowed(addr net.Addr) bool {
	h, _, e := net.SplitHostPort(addr.String())
	if e != nil {
		return false
	}
	a, e := netip.ParseAddr(h)
	if e != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range s.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (s *Server) sshConfig() *ssh.ServerConfig {
	a := ssh.SupportedAlgorithms()
	c := &ssh.ServerConfig{
		Config:                  ssh.Config{KeyExchanges: a.KeyExchanges, Ciphers: a.Ciphers, MACs: a.MACs},
		ServerVersion:           "SSH-2.0-VDISSH_" + version,
		MaxAuthTries:            3,
		PublicKeyAuthAlgorithms: []string{ssh.KeyAlgoED25519},
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != s.cfg.Username {
				return nil, errors.New("authentication failed")
			}
			keys, e := loadKeys(s.keyPath)
			if e != nil {
				return nil, errors.New("authentication unavailable")
			}
			for _, k := range keys {
				if bytes.Equal(k.Marshal(), key.Marshal()) {
					return &ssh.Permissions{Extensions: map[string]string{"fingerprint": ssh.FingerprintSHA256(key)}}, nil
				}
			}
			return nil, errors.New("authentication failed")
		},
	}
	c.AddHostKey(s.signer)
	return c
}

// Bound unresponsive handshakes, slow readers, and vanished clients. SSH
// keepalives below keep idle but reachable authenticated clients connected.
type boundedConn struct {
	net.Conn
}

func (c *boundedConn) Write(b []byte) (int, error) {
	c.Conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return c.Conn.Write(b)
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	go func() { <-ctx.Done(); ln.Close() }()
	slots := make(chan struct{}, s.cfg.MaxConnections)
	var wg sync.WaitGroup
	defer func() { cancel(); ln.Close(); wg.Wait() }()
	for {
		raw, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if !s.allowed(raw.RemoteAddr()) {
			raw.Close()
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			raw.Close()
			continue
		}
		wg.Add(1)
		go func() { defer wg.Done(); defer func() { <-slots }(); s.connection(ctx, raw) }()
	}
}

func (s *Server) connection(parent context.Context, raw net.Conn) {
	defer raw.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() { <-ctx.Done(); raw.Close() }()
	raw.SetDeadline(time.Now().Add(15 * time.Second))
	c := &boundedConn{Conn: raw}
	conn, channels, requests, err := ssh.NewServerConn(c, s.sshConfig())
	if err != nil {
		s.log.Info("connection_rejected", "peer", raw.RemoteAddr().String())
		return
	}
	defer conn.Close()
	// Read is already running inside the SSH transport after NewServerConn;
	// use a separate deadline loop, not mutable state in the reader.
	raw.SetReadDeadline(time.Time{})
	s.log.Info("authenticated", "peer", raw.RemoteAddr().String(), "key", conn.Permissions.Extensions["fingerprint"])
	go func() {
		for r := range requests {
			if r.WantReply {
				r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				raw.SetReadDeadline(time.Now().Add(70 * time.Second))
				_, _, err := conn.SendRequest("keepalive@openssh.com", true, nil)
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	var wg sync.WaitGroup
	busy := make(chan struct{}, 1)
	for nc := range channels {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.Prohibited, "only terminal sessions are supported")
			continue
		}
		select {
		case busy <- struct{}{}:
		default:
			nc.Reject(ssh.ResourceShortage, "one active session per connection")
			continue
		}
		ch, reqs, err := nc.Accept()
		if err != nil {
			<-busy
			continue
		}
		wg.Add(1)
		go func() { defer wg.Done(); defer func() { <-busy }(); s.session(ctx, ch, reqs) }()
	}
	cancel()
	wg.Wait()
	s.log.Info("disconnected", "peer", raw.RemoteAddr().String())
}

type ptyRequest struct {
	Term                                   string
	Width, Height, PixelWidth, PixelHeight uint32
	Modes                                  string
}
type windowRequest struct{ Width, Height, PixelWidth, PixelHeight uint32 }

func validSize(w, h uint32) bool { return w >= 1 && w <= 500 && h >= 1 && h <= 300 }

func (s *Server) session(ctx context.Context, ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	var width, height uint32
	var p *childProcess
	var result <-chan uint32
	requestTimer := time.NewTimer(20 * time.Second)
	defer requestTimer.Stop()
	defer func() {
		if p != nil {
			ch.Close()
			p.Kill()
			<-result
			p.Cleanup()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-requestTimer.C:
			if p == nil {
				return
			}
		case code := <-result:
			// Consumed here; replace with a closed channel for deferred cleanup.
			closed := make(chan uint32)
			close(closed)
			result = closed
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{code}))
			s.log.Info("session_end", "exit_code", code)
			return
		case r, ok := <-requests:
			if !ok {
				return
			}
			accepted := false
			switch r.Type {
			case "pty-req":
				var req ptyRequest
				if p == nil && width == 0 && ptyAvailable() && ssh.Unmarshal(r.Payload, &req) == nil && validSize(req.Width, req.Height) && len(req.Term) <= 128 {
					width, height = req.Width, req.Height
					accepted = true
				}
			case "window-change":
				var req windowRequest
				if ssh.Unmarshal(r.Payload, &req) == nil && width > 0 && validSize(req.Width, req.Height) {
					width, height = req.Width, req.Height
					accepted = p == nil || p.Resize(width, height) == nil
				}
			case "shell", "exec":
				if p != nil {
					break
				}
				var command *string
				if r.Type == "exec" {
					var req struct{ Command string }
					if ssh.Unmarshal(r.Payload, &req) != nil || !utf8.ValidString(req.Command) || !validateCommand(req.Command) {
						break
					}
					command = &req.Command
				} else if len(r.Payload) != 0 || width == 0 {
					break
				}
				child, err := startChild(command, width, height, s.cfg.WorkingDirectory)
				if err != nil {
					s.log.Error("session_start_failed", "error", err.Error())
					fmt.Fprintln(ch.Stderr(), "Could not start PowerShell. Check the server console.")
					break
				}
				p = child
				accepted = true
				requestTimer.Stop()
				result = pumpProcess(p, ch)
				s.log.Info("session_start", "kind", r.Type, "pty", width > 0, "pid", p.PID)
			case "signal":
				var req struct{ Signal string }
				if p != nil && ssh.Unmarshal(r.Payload, &req) == nil && (req.Signal == "TERM" || req.Signal == "KILL") {
					p.Kill()
					accepted = true
				}
			}
			if r.WantReply {
				r.Reply(accepted, nil)
			}
		}
	}
}

func pumpProcess(p *childProcess, ch ssh.Channel) <-chan uint32 {
	done := make(chan uint32, 1)
	go func() { io.Copy(p.Input, ch); p.Input.Close() }()
	var output sync.WaitGroup
	copyOut := func(dst io.Writer, src io.Reader) {
		output.Add(1)
		go func() {
			defer output.Done()
			if _, err := io.Copy(dst, src); err != nil {
				p.Kill()
			}
		}()
	}
	copyOut(ch, p.Output)
	if p.Error != nil {
		copyOut(ch.Stderr(), p.Error)
	}
	go func() {
		code, err := p.Wait()
		if err != nil {
			code = 1
		}
		// A client may stop reading while keeping its transport alive. Closing
		// the channel unblocks writers so final ConPTY flush cannot hang forever.
		drainTimeout := time.AfterFunc(10*time.Second, func() { ch.Close() })
		p.FinishOutput()
		output.Wait()
		drainTimeout.Stop()
		done <- code
	}()
	return done
}
