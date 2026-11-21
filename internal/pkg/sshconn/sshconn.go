// Package sshconn connects to a Docker daemon over SSH the way the docker
// CLI does: it runs "ssh host docker system dial-stdio" and speaks the
// Docker API over the command's stdin and stdout.
package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Target is a parsed ssh://[user@]host[:port] address.
type Target struct {
	User string
	Host string
	Port string
}

// Parse validates an ssh:// daemon address.
func Parse(raw string) (Target, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Target{}, err
	}
	if u.Scheme != "ssh" {
		return Target{}, fmt.Errorf("not an ssh address: %s", raw)
	}
	if u.Hostname() == "" {
		return Target{}, fmt.Errorf("no host in %s", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return Target{}, fmt.Errorf("ssh address must not have a path: %s", raw)
	}
	if _, set := u.User.Password(); set {
		return Target{}, errors.New("passwords in ssh addresses are not supported; use keys or an agent")
	}
	return Target{User: u.User.Username(), Host: u.Hostname(), Port: u.Port()}, nil
}

// Command returns the program and arguments that open a dial-stdio tunnel.
// base replaces "ssh" (for example "ssh -F ~/.ssh/other_config"); empty
// means $LAYVE_SSH_COMMAND or plain "ssh". BatchMode stops ssh from asking
// for passwords or host key confirmation, which a full-screen UI cannot show.
func (t Target) Command(base string) []string {
	if base == "" {
		base = os.Getenv("LAYVE_SSH_COMMAND")
	}
	if base == "" {
		base = "ssh"
	}
	args := strings.Fields(base)
	args = append(args, "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-T")
	if t.User != "" {
		args = append(args, "-l", t.User)
	}
	if t.Port != "" {
		args = append(args, "-p", t.Port)
	}
	return append(args, "--", t.Host, "docker", "system", "dial-stdio")
}

// Dialer returns a DialContext function for an HTTP transport: every
// connection is a new tunnel process.
func (t Target) Dialer(base string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	argv := t.Command(base)
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		return Start(ctx, argv)
	}
}

// Start runs argv and returns a connection over its stdin and stdout. The
// process outlives ctx; closing the connection stops it.
func Start(ctx context.Context, argv []string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Not CommandContext: the tunnel must outlive the dial context; Close
	// stops it.
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:noctx // lifetime is tied to the connection
	c := &conn{cmd: cmd, stderr: &limitedBuffer{max: 4096}}
	cmd.Stderr = c.stderr
	var err error
	if c.stdin, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if c.stdout, err = cmd.StdoutPipe(); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", argv[0], err)
	}
	c.exited = make(chan struct{})
	go func() {
		c.waitErr = cmd.Wait()
		close(c.exited)
	}()
	return c, nil
}

// conn is a net.Conn over a child process.
type conn struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  *limitedBuffer
	exited  chan struct{}
	waitErr error
	once    sync.Once
}

func (c *conn) Read(p []byte) (int, error) {
	n, err := c.stdout.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, c.explain(err)
	}
	if errors.Is(err, io.EOF) && c.failed() {
		return n, c.explain(io.ErrUnexpectedEOF)
	}
	return n, err
}

func (c *conn) Write(p []byte) (int, error) {
	n, err := c.stdin.Write(p)
	if err != nil {
		return n, c.explain(err)
	}
	return n, nil
}

// failed reports whether the process exited with an error.
func (c *conn) failed() bool {
	select {
	case <-c.exited:
		return c.waitErr != nil
	case <-time.After(200 * time.Millisecond):
		return false
	}
}

// explain adds what the tunnel printed on stderr, such as "Permission
// denied (publickey)" or "docker: command not found".
func (c *conn) explain(err error) error {
	msg := strings.TrimSpace(c.stderr.String())
	if msg == "" {
		return err
	}
	return fmt.Errorf("%s: %w", msg, err)
}

func (c *conn) Close() error {
	c.once.Do(func() {
		_ = c.stdin.Close()
		select {
		case <-c.exited:
		case <-time.After(2 * time.Second):
			_ = c.cmd.Process.Kill()
			<-c.exited
		}
	})
	return nil
}

func (c *conn) LocalAddr() net.Addr              { return addr{} }
func (c *conn) RemoteAddr() net.Addr             { return addr{} }
func (c *conn) SetDeadline(time.Time) error      { return nil }
func (c *conn) SetReadDeadline(time.Time) error  { return nil }
func (c *conn) SetWriteDeadline(time.Time) error { return nil }

type addr struct{}

func (addr) Network() string { return "ssh" }
func (addr) String() string  { return "ssh" }

// limitedBuffer keeps the first max bytes written to it.
type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
