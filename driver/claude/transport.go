package claude

import (
	"bufio"
	"context"
	"io"
	"os/exec"

	"github.com/Fullstop000/unio/driver"
)

// transport is the stdio boundary of one Claude child process. The abstraction
// lets integration tests exercise handle and reader logic without a real
// `claude` binary. The production implementation wraps exec.Cmd.
type transport interface {
	// stdin returns the writer for user-message JSON lines.
	stdin() io.Writer
	// stdout returns a line scanner over the child's stdout.
	stdout() *bufio.Scanner
	// wait blocks until the process exits (or the injected fake closes).
	wait() error
	// kill terminates the process.
	kill()
}

// transportFactory builds a transport for a spawn. Swapped in tests.
type transportFactory func(ctx context.Context, execPath string, args []string, spec driver.AgentSpec) (transport, error)

// --- production transport: a real `claude` child over exec.Cmd ---

type procTransport struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	sc  *bufio.Scanner
}

func (p *procTransport) stdin() io.Writer       { return p.in }
func (p *procTransport) stdout() *bufio.Scanner { return p.sc }
func (p *procTransport) wait() error            { return p.cmd.Wait() }
func (p *procTransport) kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// spawnProcTransport starts a real claude child. Stdout is line-scanned with a
// large buffer since stream-json lines (esp. tool inputs) can be long.
func spawnProcTransport(ctx context.Context, execPath string, args []string, spec driver.AgentSpec) (transport, error) {
	cmd := exec.CommandContext(ctx, execPath, args...)
	if spec.Cwd != "" {
		cmd.Dir = spec.Cwd
	}
	cmd.Env = mergeEnv(spec.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, driver.NewTransportError("claude: stdin pipe: " + err.Error())
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, driver.NewTransportError("claude: stdout pipe: " + err.Error())
	}
	if err := cmd.Start(); err != nil {
		return nil, driver.NewTransportError("claude: start: " + err.Error())
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return &procTransport{cmd: cmd, in: stdin, sc: sc}, nil
}
