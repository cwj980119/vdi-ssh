package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"
)

//go:embed web/screen.html
var screenPage embed.FS

const screenCertFile = "screen_cert.pem"
const screenKeyFile = "screen_key.pem"

func validateScreenConfig(cfg Config) error {
	if cfg.ScreenListen == "" || cfg.ScreenToken == "" {
		return errors.New("screen sharing is not configured; run screen-init --listen VDI_IP:8443 first")
	}
	host, port, err := net.SplitHostPort(cfg.ScreenListen)
	if err != nil {
		return fmt.Errorf("screen_listen must be a local IP:port: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.IsUnspecified() || ip.IsMulticast() {
		return errors.New("screen_listen must specify one local IP, not a wildcard or hostname")
	}
	if _, err = net.LookupPort("tcp", port); err != nil || port == "0" {
		return errors.New("invalid screen listening port")
	}
	if cfg.ScreenListen == cfg.Listen {
		return errors.New("screen_listen must use a different port from SSH listen")
	}
	b, err := base64.RawURLEncoding.DecodeString(cfg.ScreenToken)
	if err != nil || len(b) != 32 {
		return errors.New("invalid screen access token")
	}
	return nil
}

func ensureScreenCertificate(dir, address string) error {
	certPath, keyPath := filepath.Join(dir, screenCertFile), filepath.Join(dir, screenKeyFile)
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return err
	}
	now := time.Now()
	template := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "VDI screen sharing"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(5, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.IP(ip.AsSlice())}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	certBlock := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	keyBlock := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err = os.WriteFile(certPath, certBlock, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(keyPath, keyBlock, 0600); err != nil {
		return err
	}
	return nil
}

func serveAll(ctx context.Context, sshServer *Server, sshListener net.Listener, dataDir string, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := ensureScreenCertificate(dataDir, sshServer.cfg.ScreenListen); err != nil {
		return err
	}
	certificate, err := tls.LoadX509KeyPair(filepath.Join(dataDir, screenCertFile), filepath.Join(dataDir, screenKeyFile))
	if err != nil {
		return err
	}
	screenListener, err := tls.Listen("tcp", sshServer.cfg.ScreenListen, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	service := &screenService{token: sshServer.cfg.ScreenToken, prefixes: sshServer.prefixes, logger: logger}
	httpServer := &http.Server{Handler: service.handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	errs := make(chan error, 2)
	go func() { errs <- sshServer.Serve(ctx, sshListener) }()
	go func() {
		err := httpServer.Serve(screenListener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errs <- err
	}()
	select {
	case <-ctx.Done():
		screenListener.Close()
		httpServer.Close()
		<-errs
		<-errs
		return nil
	case err := <-errs:
		cancel()
		screenListener.Close()
		httpServer.Close()
		<-errs
		return err
	}
}

type screenService struct {
	token     string
	prefixes  []netip.Prefix
	logger    *slog.Logger
	captureMu sync.Mutex
}

func (s *screenService) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.page)
	mux.HandleFunc("/frame", s.frame)
	mux.HandleFunc("/input", s.input)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !remoteAllowed(r.RemoteAddr, s.prefixes) {
			http.Error(w, "not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'")
		mux.ServeHTTP(w, r)
	})
}

func remoteAllowed(addr string, prefixes []netip.Prefix) bool {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	for _, p := range prefixes {
		if p.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}
func (s *screenService) authenticated(r *http.Request) bool {
	provided := r.Header.Get("X-VDI-Token")
	return len(provided) == len(s.token) && subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) == 1
}

func (s *screenService) page(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	b, err := screenPage.ReadFile("web/screen.html")
	if err != nil {
		http.Error(w, "page unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}
func (s *screenService) frame(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.authenticated(r) {
		http.Error(w, "unauthorized", 401)
		return
	}
	s.captureMu.Lock()
	image, err := captureScreenJPEG(70, 1280)
	s.captureMu.Unlock()
	if err != nil {
		s.logger.Error("screen_capture_failed", "error", err.Error())
		http.Error(w, "screen capture unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store, no-cache, max-age=0")
	w.Header().Set("Content-Length", fmt.Sprint(len(image.Data)))
	w.Header().Set("X-VDI-Width", fmt.Sprint(image.Width))
	w.Header().Set("X-VDI-Height", fmt.Sprint(image.Height))
	w.Write(image.Data)
}
func (s *screenService) input(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.authenticated(r) {
		http.Error(w, "unauthorized", 401)
		return
	}
	if r.ContentLength > 4096 {
		http.Error(w, "request too large", 413)
		return
	}
	defer r.Body.Close()
	var event screenInput
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		http.Error(w, "invalid input", 400)
		return
	}
	if err := sendScreenInput(event); err != nil {
		s.logger.Info("screen_input_rejected", "error", err.Error())
		http.Error(w, "input rejected", 409)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
