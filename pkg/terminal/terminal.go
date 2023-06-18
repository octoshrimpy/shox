package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"strings"

	"github.com/liamg/shox/pkg/decorators"

	"github.com/liamg/shox/pkg/proxy"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh/terminal"
)

// Terminal communicates with the underlying terminal which is running shox
type Terminal struct {
	shell         string
	dir           string
	proxy         *proxy.Proxy
	pty           *os.File
	enableNesting bool
	hideOutput    bool
	outputMutex   sync.RWMutex
	motd          string
}

// NewTerminal creates a new terminal instance
func NewTerminal() *Terminal {

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	return &Terminal{
		shell: shell,
		proxy: proxy.NewProxy(),
	}
}

// SetShell sets the shell program being used by the terminal
func (t *Terminal) SetShell(shell string) {
	t.shell = shell
}

// SetMOTD sets the motd to write to the terminal before the main command is launched
func (t *Terminal) SetMOTD(motd string) {
	t.motd = motd
}

// SetDir sets the directory the shell will start in (CWD)
func (t *Terminal) SetDir(dir string) {
	t.dir = dir
}

// AddDecorator adds a decorator to alter the terminal output
func (t *Terminal) AddDecorator(d decorators.Decorator) {
	t.proxy.AddDecorator(d)
}

// Pty exposes the underlying terminal pty, if it exists
func (t *Terminal) Pty() *os.File {
	return t.pty
}

// SetNestingAllowed sets whether multiple shox bars can be nested inside each other
func (t *Terminal) SetNestingAllowed(allowed bool) {
	t.enableNesting = allowed
}

func (t *Terminal) ForceRedraw() {
	t.proxy.ForceRedraw()
}

// Run starts the terminal/shell proxying process
func (t *Terminal) Run(commands ...string) error {

	if !t.enableNesting {
		if os.Getenv("SHOX") != "" {
			return fmt.Errorf("shox is already running in this terminal")
		}
		_ = os.Setenv("SHOX", "1")
	}

	t.proxy.Start()
	defer t.proxy.Close()

	// if motd is command, ie: start with $( and ends with )
	motd := t.motd
	if strings.HasPrefix(t.motd, "$(") && strings.HasSuffix(t.motd, ")") {
		motd = strings.TrimPrefix(motd, "$(")
		motd = strings.TrimSuffix(motd, ")")
		output, err := exec.Command(t.shell, "-c", motd).Output()
		if err != nil {
			motd = err.Error()
		} else {
			motd = string(output)
		}
	} else {
		motd += "\n"
	}
	t.motd = motd
	
	t.proxy.Write([]byte(fmt.Sprintf("\x1bc%s\r", t.motd))) // reset term and write motd

	// Create arbitrary command.
	c := exec.Command(t.shell)

	if t.dir != "" {
		c.Dir = t.dir
	}

	// Start the command with a pty.
	var err error
	t.pty, err = pty.Start(c)
	if err != nil {
		return err
	}
	// Make sure to close the pty at the end.
	defer func() { _ = t.pty.Close() }() // Best effort.

	// Handle pty size.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {

			size, err := pty.GetsizeFull(os.Stdin)
			if err != nil {
				continue
			}

			rows, cols := t.proxy.HandleResize(size.Rows, size.Cols)
			size.Rows = rows
			size.Cols = cols

			if err := pty.Setsize(t.pty, size); err != nil {
				continue
			}

			// successful resize
		}
	}()
	ch <- syscall.SIGWINCH // Initial resize.

	// Set stdin in raw mode.
	oldState, err := terminal.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		panic(err)
	}
	defer func() { _ = terminal.Restore(int(os.Stdin.Fd()), oldState) }() // Best effort.

	for _, command := range commands {
		_, _ = t.pty.Write([]byte(command + "\n"))
	}

	// Copy stdin to the pty and the pty to stdout.
	go func() { _ = lazyCopy(t.pty, os.Stdin) }()
	go func() { _ = lazyCopy(os.Stdout, t.proxy, t.hideAllOutput) }()
	_ = lazyCopy(t.proxy, t.pty)
	fmt.Printf("\r\n")
	return nil
}

func (t *Terminal) HideOutput() {
	t.outputMutex.Lock()
	defer t.outputMutex.Unlock()
	t.hideOutput = true
}

func (t *Terminal) ShowOutput() {
	t.outputMutex.Lock()
	defer t.outputMutex.Unlock()
	t.hideOutput = false
}

func (t *Terminal) hideAllOutput() bool {
	t.outputMutex.RLock()
	defer t.outputMutex.RUnlock()
	return t.hideOutput
}
