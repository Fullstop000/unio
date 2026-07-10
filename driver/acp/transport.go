package acp

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/Fullstop000/unio/driver"
)

type stdioTransport interface {
	stdin() io.Writer
	stdout() *bufio.Scanner
	wait() error
	kill()
	errText() string
}

type transportFactory func(context.Context, string, driver.AgentSpec, []string) (stdioTransport, error)

type procTransport struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	scan   *bufio.Scanner
	stderr *boundedBuffer
}

func (p *procTransport) stdin() io.Writer       { return p.in }
func (p *procTransport) stdout() *bufio.Scanner { return p.scan }
func (p *procTransport) wait() error            { return p.cmd.Wait() }
func (p *procTransport) errText() string        { return p.stderr.String() }
func (p *procTransport) kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func spawnTransport(ctx context.Context, execPath string, spec driver.AgentSpec, args []string) (stdioTransport, error) {
	cmd := exec.CommandContext(ctx, execPath, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = mergeEnv(spec.Env)
	stderr := &boundedBuffer{limit: 64 * 1024}
	cmd.Stderr = stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, driver.NewTransportError("acp: stdin pipe: " + err.Error())
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, driver.NewTransportError("acp: stdout pipe: " + err.Error())
	}
	if err := cmd.Start(); err != nil {
		return nil, driver.NewTransportError("acp: start: " + err.Error())
	}
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	return &procTransport{cmd: cmd, in: in, scan: scanner, stderr: stderr}, nil
}

func mergeEnv(extra []string) []string {
	return append(os.Environ(), extra...)
}

type boundedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.limit <= 0 {
		return n, nil
	}
	if len(p) >= b.limit {
		b.buf.Reset()
		_, _ = b.buf.Write(p[len(p)-b.limit:])
		return n, nil
	}
	if b.buf.Len()+len(p) > b.limit {
		drop := b.buf.Len() + len(p) - b.limit
		kept := append([]byte(nil), b.buf.Bytes()[drop:]...)
		b.buf.Reset()
		_, _ = b.buf.Write(kept)
	}
	_, _ = b.buf.Write(p)
	return n, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
