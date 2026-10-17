package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
)

type PersistentShell struct {
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	isAlive      atomic.Bool
	cwd          string
	mu           sync.Mutex
	commandQueue chan *commandExecution
}

type commandExecution struct {
	command    string
	timeout    time.Duration
	resultChan chan commandResult
	ctx        context.Context
}

type commandResult struct {
	stdout      string
	stderr      string
	exitCode    int
	interrupted bool
	err         error
}

type ShellPool struct {
	mu     sync.RWMutex
	shells map[string]*PersistentShell
}

var globalPool = &ShellPool{
	shells: make(map[string]*PersistentShell),
}

// GetPersistentShell returns the shell for the default session (backwards compatibility).
func GetPersistentShell(workingDir string) *PersistentShell {
	return GetShellForSession("default", workingDir)
}

// GetShellForSession returns an isolated PersistentShell for the given sessionID.
// If the shell is dead or not yet created, a new persistent shell is started in workingDir.
func GetShellForSession(sessionID, workingDir string) *PersistentShell {
	if sessionID == "" {
		sessionID = "default"
	}

	globalPool.mu.RLock()
	s, ok := globalPool.shells[sessionID]
	if ok && s != nil && s.isAlive.Load() {
		globalPool.mu.RUnlock()
		return s
	}
	globalPool.mu.RUnlock()

	globalPool.mu.Lock()
	defer globalPool.mu.Unlock()

	// Double check under write lock
	if s, ok := globalPool.shells[sessionID]; ok && s != nil && s.isAlive.Load() {
		return s
	}

	// Clean up previous dead shell if any
	if s, ok := globalPool.shells[sessionID]; ok && s != nil {
		s.Close()
	}

	s = newPersistentShell(workingDir)
	globalPool.shells[sessionID] = s
	return s
}

// CloseShellForSession terminates and unregisters the persistent shell for a given sessionID.
func CloseShellForSession(sessionID string) {
	if sessionID == "" {
		sessionID = "default"
	}
	globalPool.mu.Lock()
	defer globalPool.mu.Unlock()

	if s, ok := globalPool.shells[sessionID]; ok && s != nil {
		s.Close()
		delete(globalPool.shells, sessionID)
	}
}

// CloseAllShells terminates all active persistent shells in the pool.
func CloseAllShells() {
	globalPool.mu.Lock()
	defer globalPool.mu.Unlock()

	for id, s := range globalPool.shells {
		if s != nil {
			s.Close()
		}
		delete(globalPool.shells, id)
	}
}

func resolveShellPath(cfg *config.Config) (string, []string) {
	if cfg != nil && cfg.Shell.Path != "" {
		args := cfg.Shell.Args
		if len(args) == 0 {
			args = []string{"-l"}
		}
		return cfg.Shell.Path, args
	}

	if envShell := os.Getenv("SHELL"); envShell != "" {
		return envShell, []string{"-l"}
	}

	if runtime.GOOS == "windows" {
		// 1. Look for bash in PATH (e.g. Git Bash, MSYS2)
		if bashPath, err := exec.LookPath("bash"); err == nil {
			return bashPath, []string{"-l"}
		}
		// 2. Look for standard Git for Windows installations
		gitCandidates := []string{
			`C:\Program Files\Git\bin\bash.exe`,
			`C:\Program Files (x86)\Git\bin\bash.exe`,
			`C:\Program Files\Git\usr\bin\bash.exe`,
		}
		for _, cand := range gitCandidates {
			if _, err := os.Stat(cand); err == nil {
				return cand, []string{"-l"}
			}
		}
		// 3. Fallback to WSL bash if available
		if wslPath, err := exec.LookPath("wsl"); err == nil {
			return wslPath, []string{"bash", "-l"}
		}
		// 4. Fallback to PowerShell
		if psPath, err := exec.LookPath("powershell"); err == nil {
			return psPath, []string{"-NoLogo", "-NoExit", "-Command", "-"}
		}
		return "cmd.exe", []string{"/Q"}
	}

	// Unix defaults
	if bashPath, err := exec.LookPath("bash"); err == nil {
		return bashPath, []string{"-l"}
	}
	if shPath, err := exec.LookPath("sh"); err == nil {
		return shPath, []string{"-l"}
	}
	return "/bin/bash", []string{"-l"}
}

func newPersistentShell(cwd string) *PersistentShell {
	cfg := config.Get()
	shellPath, shellArgs := resolveShellPath(cfg)

	cmd := exec.Command(shellPath, shellArgs...)
	cmd.Dir = cwd
	prepareCmd(cmd)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}

	cmd.Env = append(os.Environ(), "GIT_EDITOR=true")

	err = cmd.Start()
	if err != nil {
		return nil
	}

	shell := &PersistentShell{
		cmd:          cmd,
		stdin:        stdinPipe,
		cwd:          cwd,
		commandQueue: make(chan *commandExecution, 10),
	}
	shell.isAlive.Store(true)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "Panic in shell command processor: %v\n", r)
				shell.isAlive.Store(false)
			}
		}()
		shell.processCommands()
	}()

	go func() {
		_ = cmd.Wait()
		shell.isAlive.Store(false)
	}()

	return shell
}

func (s *PersistentShell) processCommands() {
	for cmd := range s.commandQueue {
		result := s.execCommand(cmd.command, cmd.timeout, cmd.ctx)
		cmd.resultChan <- result
	}
}

func (s *PersistentShell) execCommand(command string, timeout time.Duration, ctx context.Context) commandResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isAlive.Load() {
		return commandResult{
			stderr:   "Shell is not alive",
			exitCode: 1,
			err:      errors.New("shell is not alive"),
		}
	}

	tempDir := os.TempDir()
	stdoutFile := filepath.Join(tempDir, fmt.Sprintf("opencode-stdout-%d", time.Now().UnixNano()))
	stderrFile := filepath.Join(tempDir, fmt.Sprintf("opencode-stderr-%d", time.Now().UnixNano()))
	statusFile := filepath.Join(tempDir, fmt.Sprintf("opencode-status-%d", time.Now().UnixNano()))
	cwdFile := filepath.Join(tempDir, fmt.Sprintf("opencode-cwd-%d", time.Now().UnixNano()))

	defer func() {
		os.Remove(stdoutFile)
		os.Remove(stderrFile)
		os.Remove(statusFile)
		os.Remove(cwdFile)
	}()

	fullCommand := fmt.Sprintf(`
eval %s < /dev/null > %s 2> %s
EXEC_EXIT_CODE=$?
pwd > %s
echo $EXEC_EXIT_CODE > %s
`,
		shellQuote(command),
		shellQuote(stdoutFile),
		shellQuote(stderrFile),
		shellQuote(cwdFile),
		shellQuote(statusFile),
	)

	_, err := s.stdin.Write([]byte(fullCommand + "\n"))
	if err != nil {
		return commandResult{
			stderr:   fmt.Sprintf("Failed to write command to shell: %v", err),
			exitCode: 1,
			err:      err,
		}
	}

	interrupted := false

	startTime := time.Now()

	done := make(chan bool)
	go func() {
		for {
			select {
			case <-ctx.Done():
				s.killChildren()
				interrupted = true
				done <- true
				return

			case <-time.After(10 * time.Millisecond):
				if fileExists(statusFile) && fileSize(statusFile) > 0 {
					done <- true
					return
				}

				if timeout > 0 {
					elapsed := time.Since(startTime)
					if elapsed > timeout {
						s.killChildren()
						interrupted = true
						done <- true
						return
					}
				}
			}
		}
	}()

	<-done

	stdout := readFileOrEmpty(stdoutFile)
	stderr := readFileOrEmpty(stderrFile)
	exitCodeStr := readFileOrEmpty(statusFile)
	newCwd := readFileOrEmpty(cwdFile)

	exitCode := 0
	if exitCodeStr != "" {
		fmt.Sscanf(exitCodeStr, "%d", &exitCode)
	} else if interrupted {
		exitCode = 143
		stderr += "\nCommand execution timed out or was interrupted"
	}

	if newCwd != "" {
		s.cwd = strings.TrimSpace(newCwd)
	}

	return commandResult{
		stdout:      stdout,
		stderr:      stderr,
		exitCode:    exitCode,
		interrupted: interrupted,
	}
}

func (s *PersistentShell) killChildren() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}

	if runtime.GOOS == "windows" {
		killCmd := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", s.cmd.Process.Pid))
		prepareCmd(killCmd)
		_ = killCmd.Run()
		return
	}

	pgrepCmd := exec.Command("pgrep", "-P", fmt.Sprintf("%d", s.cmd.Process.Pid))
	output, err := pgrepCmd.Output()
	if err != nil {
		return
	}

	for pidStr := range strings.SplitSeq(string(output), "\n") {
		if pidStr = strings.TrimSpace(pidStr); pidStr != "" {
			var pid int
			fmt.Sscanf(pidStr, "%d", &pid)
			if pid > 0 {
				proc, err := os.FindProcess(pid)
				if err == nil {
					proc.Signal(syscall.SIGTERM)
				}
			}
		}
	}
}

func (s *PersistentShell) Exec(ctx context.Context, command string, timeoutMs int) (string, string, int, bool, error) {
	if !s.isAlive.Load() {
		return "", "Shell is not alive", 1, false, errors.New("shell is not alive")
	}

	timeout := time.Duration(timeoutMs) * time.Millisecond

	resultChan := make(chan commandResult)
	s.commandQueue <- &commandExecution{
		command:    command,
		timeout:    timeout,
		resultChan: resultChan,
		ctx:        ctx,
	}

	result := <-resultChan
	return result.stdout, result.stderr, result.exitCode, result.interrupted, result.err
}

func (s *PersistentShell) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isAlive.Swap(false) {
		return
	}

	if s.stdin != nil {
		_, _ = s.stdin.Write([]byte("exit\n"))
		_ = s.stdin.Close()
	}

	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func readFileOrEmpty(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(content)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
