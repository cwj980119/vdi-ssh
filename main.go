package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const version = "0.2.0"

type Config struct {
	Listen           string   `json:"listen"`
	Username         string   `json:"username"`
	AllowFrom        []string `json:"allow_from"`
	WorkingDirectory string   `json:"working_directory"`
	MaxConnections   int      `json:"max_connections"`
	ScreenListen     string   `json:"screen_listen,omitempty"`
	ScreenToken      string   `json:"screen_token,omitempty"`
}

func defaultDataDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return "vdi-ssh-data"
	}
	return filepath.Join(base, "VDISSH")
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Println(`VDI SSH ` + version + ` - portable Windows SSH server

  vdi-ssh init --public-key CLIENT.pub --listen VDI_IP:2222 --allow-ip CLIENT_IP
  vdi-ssh screen-init --listen VDI_IP:8443
  vdi-ssh serve              # SSH, SFTP/SCP
  vdi-ssh serve-all          # SSH, SFTP/SCP, screen sharing
  vdi-ssh doctor [--target VDI_IP:2222]

Use --data-dir DIR with init/serve to select a configuration directory.
Public-key authentication only. Commands run as the Windows account which
starts this program. Keep the server window open; Ctrl+C stops it.
See README.ko.md for setup and supported features.`)
		return nil
	}
	switch args[0] {
	case "init":
		return initialize(args[1:])
	case "serve":
		return serveCommand(args[1:])
	case "serve-all":
		return serveAllCommand(args[1:])
	case "screen-init":
		return screenInitialize(args[1:])
	case "doctor":
		return doctor(args[1:])
	case "version":
		fmt.Println(version)
		return nil
	default:
		return fmt.Errorf("unknown command %q; run vdi-ssh help", args[0])
	}
}

func initialize(args []string) error {
	f := flag.NewFlagSet("init", flag.ContinueOnError)
	dir := f.String("data-dir", defaultDataDir(), "private configuration directory (created once)")
	pub := f.String("public-key", "", "client PUBLIC key file")
	listen := f.String("listen", "127.0.0.1:2222", "specific local IP:port")
	allow := f.String("allow-ip", "127.0.0.1", "allowed client IPs/CIDRs, comma separated")
	name := f.String("username", "vdi", "SSH login alias; does not switch Windows accounts")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *pub == "" {
		return errors.New("--public-key is required; copy only the client's .pub file")
	}
	data, err := os.ReadFile(*pub)
	if err != nil {
		return err
	}
	keys, err := parseAuthorizedKeys(data)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cfg := Config{Listen: *listen, Username: *name, AllowFrom: strings.Split(*allow, ","), WorkingDirectory: home, MaxConnections: 4}
	if _, err = validateConfig(cfg); err != nil {
		return err
	}
	*dir, err = filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if _, err = os.Stat(*dir); !os.IsNotExist(err) {
		return errors.New("data directory already exists; use a new --data-dir to avoid overwriting keys")
	}
	if err = os.MkdirAll(filepath.Dir(*dir), 0700); err != nil {
		return err
	}
	if err = makePrivateDirectory(*dir); err != nil {
		return err
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(private, "VDI SSH host key")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(*dir, "host_ed25519"), pem.EncodeToMemory(block), 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(*dir, "authorized_keys"), data, 0600); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(*dir, "config.json"), append(encoded, '\n'), 0600); err != nil {
		return err
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return err
	}
	fmt.Println("Created:", *dir)
	fmt.Println("Host fingerprint:", ssh.FingerprintSHA256(signer.PublicKey()))
	fmt.Printf("Authorized keys: %d\nListen: %s\nSSH alias: %s\n", len(keys), cfg.Listen, cfg.Username)
	fmt.Printf("Start with: .\\vdi-ssh.exe serve --data-dir '%s'\n", strings.ReplaceAll(*dir, "'", "''"))
	return nil
}

func screenInitialize(args []string) error {
	f := flag.NewFlagSet("screen-init", flag.ContinueOnError)
	dir := f.String("data-dir", defaultDataDir(), "configuration directory")
	listen := f.String("listen", "", "specific VDI IP:HTTPS port, for example 10.20.30.40:8443")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *listen == "" {
		return errors.New("--listen is required")
	}
	cfg, err := loadConfig(*dir)
	if err != nil {
		return err
	}
	cfg.ScreenListen = *listen
	if _, err := validateConfig(cfg); err != nil {
		return err
	}
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		return err
	}
	cfg.ScreenToken = base64.RawURLEncoding.EncodeToString(tokenBytes)
	if err = validateScreenConfig(cfg); err != nil { return err }
	if err = ensureScreenCertificate(*dir, cfg.ScreenListen); err != nil {
		return err
	}
	if err = writeConfig(*dir, cfg); err != nil { return err }
	addressHash := sha256.Sum256([]byte(cfg.ScreenToken))
	fmt.Printf("Screen sharing is configured on https://%s\n", cfg.ScreenListen)
	fmt.Printf("Open after serve-all: https://%s/#%s\n", cfg.ScreenListen, cfg.ScreenToken)
	fmt.Printf("Screen access token SHA-256: %x\n", addressHash[:])
	return nil
}

func parseAuthorizedKeys(data []byte) ([]ssh.PublicKey, error) {
	if len(data) > 1024*1024 {
		return nil, errors.New("authorized_keys exceeds 1 MiB")
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	var keys []ssh.PublicKey
	for i, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		key, _, options, rest, err := ssh.ParseAuthorizedKey(line)
		if err != nil || len(bytes.TrimSpace(rest)) > 0 {
			return nil, fmt.Errorf("invalid authorized_keys line %d; use an OpenSSH .pub file", i+1)
		}
		if len(options) > 0 {
			return nil, fmt.Errorf("authorized_keys line %d has unsupported options; options are never silently ignored", i+1)
		}
		if _, ok := key.(*ssh.Certificate); ok {
			return nil, errors.New("SSH certificates are not supported; use a plain public key")
		}
		if key.Type() != ssh.KeyAlgoED25519 {
			return nil, errors.New("this version accepts Ed25519 client keys only; generate with ssh-keygen -t ed25519")
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, errors.New("no public keys found")
	}
	return keys, nil
}

func validateConfig(cfg Config) ([]netip.Prefix, error) {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen must be a local IP:port: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.IsUnspecified() || ip.IsMulticast() {
		return nil, errors.New("listen must specify one local IP, not a wildcard or hostname")
	}
	if _, err = net.LookupPort("tcp", port); err != nil || port == "0" {
		return nil, errors.New("invalid listening port")
	}
	if len(cfg.Username) == 0 || len(cfg.Username) > 64 || strings.ContainsAny(cfg.Username, " \r\n\t@\\\"'") {
		return nil, errors.New("invalid SSH username alias")
	}
	if cfg.MaxConnections < 1 || cfg.MaxConnections > 16 {
		return nil, errors.New("max_connections must be 1..16")
	}
	info, err := os.Stat(cfg.WorkingDirectory)
	if err != nil || !info.IsDir() {
		return nil, errors.New("working_directory must be an existing directory")
	}
	var prefixes []netip.Prefix
	for _, raw := range cfg.AllowFrom {
		raw = strings.TrimSpace(raw)
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			a, e := netip.ParseAddr(raw)
			if e != nil {
				return nil, fmt.Errorf("invalid allow_from IP/CIDR %q", raw)
			}
			a = a.Unmap()
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if p.Bits() == 0 {
			return nil, errors.New("allow_from cannot allow the whole internet (/0)")
		}
		prefixes = append(prefixes, p.Masked())
	}
	if len(prefixes) == 0 {
		return nil, errors.New("allow_from must contain at least one client IP/CIDR")
	}
	return prefixes, nil
}

func loadConfig(dir string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return Config{}, errors.New("config.json contains trailing data")
	}
	return cfg, nil
}

func writeConfig(dir string, cfg Config) error {
	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), append(encoded, '\n'), 0600)
}

func serveCommand(args []string) error {
	return runServices(args, false)
}

func serveAllCommand(args []string) error {
	return runServices(args, true)
}

func runServices(args []string, withScreen bool) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	dir := f.String("data-dir", defaultDataDir(), "configuration directory")
	if err := f.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*dir)
	if err != nil {
		return err
	}
	prefixes, err := validateConfig(cfg)
	if err != nil {
		return err
	}
	keyData, err := os.ReadFile(filepath.Join(*dir, "host_ed25519"))
	if err != nil {
		return err
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return err
	}
	keyPath := filepath.Join(*dir, "authorized_keys")
	if _, err = loadKeys(keyPath); err != nil {
		return err
	}
	w, err := newAuditWriter(filepath.Join(*dir, "audit.jsonl"))
	if err != nil {
		return err
	}
	defer w.Close()
	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stdout, w), nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	account, _ := user.Current()
	identity := "unknown"
	if account != nil {
		identity = account.Username
	}
	fmt.Printf("VDI SSH %s\nWindows account: %s\nHost fingerprint: %s\n", version, identity, ssh.FingerprintSHA256(signer.PublicKey()))
	fmt.Printf("SSH/SFTP/SCP: %s@%s. Ctrl+C stops the server.\n", cfg.Username, cfg.Listen)
	logger.Info("server_start", "listen", cfg.Listen, "windows_account", identity)
	s := &Server{cfg: cfg, prefixes: prefixes, keyPath: keyPath, signer: signer, log: logger}
	if !withScreen {
		err = s.Serve(ctx, listener)
	} else {
		if err = validateScreenConfig(cfg); err != nil {
			listener.Close()
			return err
		}
		fmt.Printf("Screen: https://%s/#%s\n", cfg.ScreenListen, cfg.ScreenToken)
		err = serveAll(ctx, s, listener, *dir, logger)
	}
	logger.Info("server_stop")
	return err
}

func loadKeys(path string) ([]ssh.PublicKey, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil {
		return nil, err
	}
	return parseAuthorizedKeys(b)
}

func doctor(args []string) error {
	f := flag.NewFlagSet("doctor", flag.ContinueOnError)
	target := f.String("target", "", "optional single IP:port to test")
	if err := f.Parse(args); err != nil {
		return err
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	fmt.Println("Windows account:", u.Username)
	fmt.Println("ConPTY available:", ptyAvailable())
	fmt.Println("Configuration directory:", defaultDataDir())
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return err
	}
	for _, a := range addresses {
		fmt.Println("Local address:", a.String())
	}
	if *target != "" {
		h, _, err := net.SplitHostPort(*target)
		if err != nil {
			return err
		}
		if _, err = netip.ParseAddr(h); err != nil {
			return errors.New("target must be an explicit IP:port")
		}
		c, err := net.DialTimeout("tcp", *target, 3*time.Second)
		if err != nil {
			return fmt.Errorf("TCP connection failed (listener, firewall, or route): %w", err)
		}
		c.Close()
		fmt.Println("TCP connected; this does not verify SSH authentication.")
	}
	return nil
}

type auditWriter struct {
	mu   sync.Mutex
	path string
	file *os.File
	size int64
}

func newAuditWriter(path string) (*auditWriter, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	i, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	return &auditWriter{path: path, file: f, size: i.Size()}, nil
}
func (w *auditWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > 4*1024*1024 {
		if err := w.file.Close(); err != nil {
			return 0, err
		}
		if err := os.Remove(w.path + ".1"); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		if err := os.Rename(w.path, w.path+".1"); err != nil {
			return 0, err
		}
		f, e := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY, 0600)
		if e != nil {
			return 0, e
		}
		w.file = f
		w.size = 0
	}
	n, e := w.file.Write(p)
	w.size += int64(n)
	return n, e
}
func (w *auditWriter) Close() error { w.mu.Lock(); defer w.mu.Unlock(); return w.file.Close() }
