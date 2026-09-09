package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/windows"
)

func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, k, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	s, e := ssh.NewSignerFromKey(k)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

type testHost struct {
	s    *Server
	addr string
	key  ssh.Signer
	host ssh.Signer
	dir  string
}

func newTestHost(t *testing.T) *testHost {
	return newTestHostWithPrefix(t, "127.0.0.1/32")
}

func newTestHostWithPrefix(t *testing.T, prefix string) *testHost {
	t.Helper()
	d := t.TempDir()
	key := testSigner(t)
	host := testSigner(t)
	kp := filepath.Join(d, "authorized_keys")
	if e := os.WriteFile(kp, ssh.MarshalAuthorizedKey(key.PublicKey()), 0600); e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	s := &Server{cfg: Config{Username: "vdi", WorkingDirectory: d, MaxConnections: 4}, prefixes: []netip.Prefix{netip.MustParsePrefix(prefix)}, keyPath: kp, signer: host, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(15 * time.Second):
			t.Error("server shutdown hung")
		}
	})
	return &testHost{s, ln.Addr().String(), key, host, d}
}
func (h *testHost) connect(user string, key ssh.Signer) (*ssh.Client, error) {
	return ssh.Dial("tcp", h.addr, &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(h.host.PublicKey()), Timeout: 3 * time.Second})
}
func mustSession(t *testing.T, h *testHost) (*ssh.Client, *ssh.Session) {
	t.Helper()
	c, e := h.connect("vdi", h.key)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	s, e := c.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return c, s
}

func TestPublicKeyAuthenticationAndRevocation(t *testing.T) {
	h := newTestHost(t)
	for _, test := range []struct {
		name string
		key  ssh.Signer
	}{{"other", h.key}, {"vdi", testSigner(t)}} {
		c, e := h.connect(test.name, test.key)
		if e == nil {
			c.Close()
			t.Fatal("unauthorized login accepted")
		}
	}
	c, e := h.connect("vdi", h.key)
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	if e = os.WriteFile(h.s.keyPath, ssh.MarshalAuthorizedKey(testSigner(t).PublicKey()), 0600); e != nil {
		t.Fatal(e)
	}
	if c, e = h.connect("vdi", h.key); e == nil {
		c.Close()
		t.Fatal("revoked key accepted")
	}
}

func TestPowerShellExecUnicodeStdinAndExit(t *testing.T) {
	h := newTestHost(t)
	_, s := mustSession(t, h)
	s.Stdin = strings.NewReader("input-from-client\n")
	out, e := s.CombinedOutput(`Write-Output '한글-SSH'; [Console]::WriteLine([Console]::ReadLine()); exit 7`)
	if ee, ok := e.(*ssh.ExitError); !ok || ee.ExitStatus() != 7 {
		t.Fatalf("exit status: %v; output=%s", e, out)
	}
	for _, want := range []string{"한글-SSH", "input-from-client"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
}

func TestRejectForwardingSubsystemAndDuplicateStart(t *testing.T) {
	h := newTestHost(t)
	c, s := mustSession(t, h)
	if ch, _, e := c.OpenChannel("direct-tcpip", nil); e == nil {
		ch.Close()
		t.Fatal("forwarding accepted")
	}
	if e := s.RequestSubsystem("sftp"); e == nil {
		t.Fatal("SFTP accepted")
	}
	if e := s.RequestPty("xterm", 400, 600, ssh.TerminalModes{}); e == nil {
		t.Fatal("oversized terminal accepted")
	}
	if e := s.Start("Start-Sleep 30"); e != nil {
		t.Fatal(e)
	}
	ok, e := s.SendRequest("exec", true, ssh.Marshal(struct{ Command string }{"Write-Output duplicate"}))
	if e != nil || ok {
		t.Fatalf("duplicate exec: %v %v", ok, e)
	}
}

func TestDisconnectKillsEntireProcessTree(t *testing.T) {
	h := newTestHost(t)
	c, s := mustSession(t, h)
	pidFile := filepath.Join(h.dir, "child-pid.txt")
	command := `$p = Start-Process -FilePath "$PSHOME\powershell.exe" -ArgumentList '-NoProfile','-NonInteractive','-Command','Start-Sleep 300' -WindowStyle Hidden -PassThru; [IO.File]::WriteAllText('` + strings.ReplaceAll(pidFile, "'", "''") + `', [string]$p.Id); Start-Sleep 300`
	if e := s.Start(command); e != nil {
		t.Fatal(e)
	}
	var pid uint32
	until := time.Now().Add(8 * time.Second)
	for time.Now().Before(until) {
		b, _ := os.ReadFile(pidFile)
		if _, e := fmt.Sscan(string(b), &pid); e == nil && pid > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child did not report PID")
	}
	process, e := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if e != nil {
		t.Fatal(e)
	}
	defer windows.CloseHandle(process)
	c.Close()
	state, e := windows.WaitForSingleObject(process, 8000)
	if e != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant survived disconnect: %v %v", state, e)
	}
}

func TestInteractivePTYResizeUnicode(t *testing.T) {
	if !ptyAvailable() {
		t.Skip("ConPTY unavailable")
	}
	// The test runner injects its own PSReadLine module path. Exercise the
	// installed Windows module without changing execution policy or trust.
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PSModulePath", filepath.Join(system, "WindowsPowerShell", "v1.0", "Modules"))
	h := newTestHost(t)
	_, s := mustSession(t, h)
	if e := s.RequestPty("xterm-256color", 30, 100, ssh.TerminalModes{}); e != nil {
		t.Fatal(e)
	}
	in, e := s.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	out, e := s.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Shell(); e != nil {
		t.Fatal(e)
	}
	if e = s.WindowChange(40, 120); e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	var buf bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		b := make([]byte, 2048)
		for {
			n, e := out.Read(b)
			if n > 0 {
				mu.Lock()
				buf.Write(b[:n])
				mu.Unlock()
				if bytes.Contains(b[:n], []byte("\x1b[6n")) {
					in.Write([]byte("\x1b[1;1R"))
				}
			}
			if e != nil {
				return
			}
		}
	}()
	// Wait for the PowerShell prompt before sending a line, as a real terminal does.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ready := strings.Contains(buf.String(), "PS ")
		mu.Unlock()
		if ready {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	io.WriteString(in, "Write-Output ('PTY_' + 'SUCCESS_한글')\r")
	time.Sleep(300 * time.Millisecond)
	io.WriteString(in, "exit 0\r")
	ended := make(chan error, 1)
	go func() { ended <- s.Wait() }()
	select {
	case e := <-ended:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(12 * time.Second):
		mu.Lock()
		text := buf.String()
		mu.Unlock()
		t.Fatalf("PTY hung: %q", text)
	}
	<-drained
	mu.Lock()
	text := buf.String()
	mu.Unlock()
	if !strings.Contains(text, "PTY_SUCCESS_한글") {
		t.Fatalf("PTY output missing: %q", text)
	}
}

func TestOpenSSHClientCompatibility(t *testing.T) {
	sshExe, e := exec.LookPath("ssh.exe")
	if e != nil {
		t.Skip("OpenSSH client unavailable")
	}
	h := newTestHost(t)
	_, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := ssh.NewSignerFromKey(private)
	if e != nil {
		t.Fatal(e)
	}
	block, e := ssh.MarshalPrivateKey(private, "")
	if e != nil {
		t.Fatal(e)
	}
	privateDir := filepath.Join(h.dir, "private")
	if e = makePrivateDirectory(privateDir); e != nil {
		t.Fatal(e)
	}
	privatePath := filepath.Join(privateDir, "identity")
	if e = os.WriteFile(privatePath, pem.EncodeToMemory(block), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(h.s.keyPath, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0600); e != nil {
		t.Fatal(e)
	}
	host, port, _ := net.SplitHostPort(h.addr)
	known := filepath.Join(h.dir, "known_hosts")
	entry := "[" + host + "]:" + port + " " + string(ssh.MarshalAuthorizedKey(h.host.PublicKey()))
	if e = os.WriteFile(known, []byte(entry), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sshExe, "-F", "NUL", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+known, "-i", privatePath, "-p", port, "vdi@"+host, "Write-Output 'OPENSSH_OK'; exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, e := cmd.CombinedOutput()
	if e != nil || !bytes.Contains(out, []byte("OPENSSH_OK")) {
		t.Fatalf("OpenSSH: %v: %s", e, out)
	}
}

func TestStrictKeyAndConfigValidation(t *testing.T) {
	key := ssh.MarshalAuthorizedKey(testSigner(t).PublicKey())
	for _, input := range [][]byte{nil, []byte("invalid"), append([]byte("command=\"whoami\" "), key...), append([]byte("no-pty "), key...)} {
		if _, e := parseAuthorizedKeys(input); e == nil {
			t.Fatal("invalid/options key accepted")
		}
	}
	if _, e := parseAuthorizedKeys(append([]byte{0xef, 0xbb, 0xbf}, key...)); e != nil {
		t.Fatal(e)
	}
	cfg := Config{Listen: "127.0.0.1:2222", Username: "vdi", AllowFrom: []string{"127.0.0.1"}, WorkingDirectory: t.TempDir(), MaxConnections: 4}
	if _, e := validateConfig(cfg); e != nil {
		t.Fatal(e)
	}
	cfg.Listen = "0.0.0.0:2222"
	if _, e := validateConfig(cfg); e == nil {
		t.Fatal("wildcard bind accepted")
	}
	cfg.Listen = "127.0.0.1:2222"
	cfg.AllowFrom = []string{"0.0.0.0/0"}
	if _, e := validateConfig(cfg); e == nil {
		t.Fatal("allow-any accepted")
	}
}

func TestSourceIPDeniedBeforeAuthentication(t *testing.T) {
	h := newTestHostWithPrefix(t, "192.0.2.1/32")
	if c, err := h.connect("vdi", h.key); err == nil {
		c.Close()
		t.Fatal("disallowed IP authenticated")
	}
}

func TestInitPreservesHostIdentityAndProtectsDirectory(t *testing.T) {
	base := t.TempDir()
	pub := filepath.Join(base, "client.pub")
	dir := filepath.Join(base, "private")
	if err := os.WriteFile(pub, ssh.MarshalAuthorizedKey(testSigner(t).PublicKey()), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--data-dir", dir, "--public-key", pub}
	if err := initialize(args); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(dir, "host_ed25519"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ssh.ParsePrivateKey(key); err != nil {
		t.Fatal(err)
	}
	if err = initialize(args); err == nil {
		t.Fatal("existing host identity overwritten")
	}
	after, err := os.ReadFile(filepath.Join(dir, "host_ed25519"))
	if err != nil || !bytes.Equal(key, after) {
		t.Fatal("host key changed")
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("directory DACL not protected: %v", err)
	}
}
