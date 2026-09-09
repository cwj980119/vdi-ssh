package main

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var kernel = windows.NewLazySystemDLL("kernel32.dll")
var updateAttribute = kernel.NewProc("UpdateProcThreadAttribute")

func ptyAvailable() bool { return kernel.NewProc("CreatePseudoConsole").Find() == nil }

func makePrivateDirectory(path string) error {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + u.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.CreateDirectory(p, &sa)
}

// A job owns each session's complete process tree. The initial process is created
// suspended and assigned before its first instruction, so children cannot escape
// cleanup by racing assignment. The job handle is not inherited.
type childProcess struct {
	Input      *os.File
	Output     *os.File
	Error      *os.File
	process    windows.Handle
	job        windows.Handle
	pty        windows.Handle
	PID        uint32
	mu         sync.Mutex
	finishOnce sync.Once
}

func shellArgs(command *string) (string, []string, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return "", nil, err
	}
	shell := filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe")
	if command == nil {
		return shell, []string{"-NoLogo", "-NoProfile", "-NoExit"}, nil
	}
	// Encoding carries the exact command without interpolation into a launcher.
	// The command is intentionally evaluated by PowerShell after authentication.
	script := "$ProgressPreference = 'SilentlyContinue'; [Console]::InputEncoding = [Console]::OutputEncoding = $OutputEncoding = [System.Text.UTF8Encoding]::new($false); " + *command
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, v := range u {
		binary.LittleEndian.PutUint16(b[i*2:], v)
	}
	return shell, []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(b)}, nil
}

func startChild(command *string, width, height uint32, directory string) (p *childProcess, err error) {
	shell, args, err := shellArgs(command)
	if err != nil {
		return nil, err
	}
	line := windows.EscapeArg(shell)
	for _, a := range args {
		line += " " + windows.EscapeArg(a)
	}
	if len(utf16.Encode([]rune(line))) > 30000 {
		return nil, errors.New("command is too long for Windows process creation")
	}
	app, err := windows.UTF16PtrFromString(shell)
	if err != nil {
		return nil, err
	}
	cmdline, err := windows.UTF16PtrFromString(line)
	if err != nil {
		return nil, err
	}
	cwd, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return nil, err
	}
	p = &childProcess{}
	var childEnds []*os.File
	defer func() {
		for _, f := range childEnds {
			f.Close()
		}
		if err != nil {
			p.Cleanup()
		}
	}()
	p.job, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		return p, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(p.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		return p, err
	}
	stdin, writer, err := os.Pipe()
	if err != nil {
		return p, err
	}
	p.Input = writer
	childEnds = append(childEnds, stdin)
	reader, stdout, err := os.Pipe()
	if err != nil {
		return p, err
	}
	p.Output = reader
	childEnds = append(childEnds, stdout)
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return p, err
	}
	defer attrs.Delete()
	si := windows.StartupInfoEx{}
	si.Cb = uint32(unsafe.Sizeof(si))
	si.ProcThreadAttributeList = attrs.List()
	flags := uint32(windows.CREATE_SUSPENDED | windows.EXTENDED_STARTUPINFO_PRESENT)
	inherit := false
	if width > 0 {
		// Prevent inheriting the server's redirected standard handles. ConPTY
		// supplies the attached process's console handles.
		si.Flags = windows.STARTF_USESTDHANDLES
		if !ptyAvailable() {
			return p, errors.New("ConPTY requires Windows 10 1809 / Server 2019 or later")
		}
		err = windows.CreatePseudoConsole(windows.Coord{X: int16(width), Y: int16(height)}, windows.Handle(stdin.Fd()), windows.Handle(stdout.Fd()), 0, &p.pty)
		if err != nil {
			return p, err
		}
		// PSEUDOCONSOLE takes the handle VALUE, unlike other attribute values.
		ok, _, e := updateAttribute.Call(uintptr(unsafe.Pointer(attrs.List())), 0, windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, uintptr(p.pty), unsafe.Sizeof(p.pty), 0, 0)
		if ok == 0 {
			return p, fmt.Errorf("attach ConPTY: %w", e)
		}
	} else {
		errReader, stderr, e := os.Pipe()
		if e != nil {
			return p, e
		}
		p.Error = errReader
		childEnds = append(childEnds, stderr)
		handles := []windows.Handle{windows.Handle(stdin.Fd()), windows.Handle(stdout.Fd()), windows.Handle(stderr.Fd())}
		for _, h := range handles {
			if err = windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
				return p, err
			}
		}
		if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
			return p, err
		}
		si.Flags = windows.STARTF_USESTDHANDLES
		si.StdInput = handles[0]
		si.StdOutput = handles[1]
		si.StdErr = handles[2]
		flags |= windows.CREATE_NO_WINDOW
		inherit = true
	}
	var pi windows.ProcessInformation
	err = windows.CreateProcess(app, cmdline, nil, nil, inherit, flags, nil, cwd, &si.StartupInfo, &pi)
	runtime.KeepAlive(attrs)
	if err != nil {
		return p, err
	}
	p.process = pi.Process
	p.PID = pi.ProcessId
	defer windows.CloseHandle(pi.Thread)
	if err = windows.AssignProcessToJobObject(p.job, p.process); err != nil {
		windows.TerminateProcess(p.process, 1)
		return p, fmt.Errorf("cannot attach session job (VDI job restrictions): %w", err)
	}
	if _, err = windows.ResumeThread(pi.Thread); err != nil {
		return p, err
	}
	return p, nil
}

func (p *childProcess) Wait() (uint32, error) {
	_, err := windows.WaitForSingleObject(p.process, windows.INFINITE)
	if err != nil {
		return 1, err
	}
	var code uint32
	err = windows.GetExitCodeProcess(p.process, &code)
	return code, err
}

func (p *childProcess) Resize(w, h uint32) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pty == 0 {
		return errors.New("no PTY")
	}
	return windows.ResizePseudoConsole(p.pty, windows.Coord{X: int16(w), Y: int16(h)})
}

func (p *childProcess) Kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != 0 {
		windows.TerminateJobObject(p.job, 1)
	}
}

// FinishOutput must run while another goroutine drains Output: closing a
// pseudoconsole can synchronously flush final terminal output.
func (p *childProcess) FinishOutput() {
	p.finishOnce.Do(func() {
		p.Kill()
		if p.Input != nil {
			p.Input.Close()
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.pty != 0 {
			windows.ClosePseudoConsole(p.pty)
			p.pty = 0
		}
	})
}

func (p *childProcess) Cleanup() {
	if p == nil {
		return
	}
	p.FinishOutput()
	for _, f := range []*os.File{p.Output, p.Error} {
		if f != nil {
			f.Close()
		}
	}
	if p.process != 0 {
		windows.CloseHandle(p.process)
		p.process = 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != 0 {
		windows.CloseHandle(p.job)
		p.job = 0
	}
}

func validateCommand(s string) bool { return len(s) <= 12000 && !strings.ContainsRune(s, 0) }
