package sshconn

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	got, err := Parse("ssh://deploy@10.0.0.5:2222")
	if err != nil || got != (Target{User: "deploy", Host: "10.0.0.5", Port: "2222"}) {
		t.Fatalf("Parse = %+v, %v", got, err)
	}
	for _, bad := range []string{"tcp://h:1", "ssh://", "ssh://h/var/run/docker.sock", "ssh://u:pw@h"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
}

func TestCommand(t *testing.T) {
	tg := Target{User: "root", Host: "remote", Port: "2222"}
	got := strings.Join(tg.Command("ssh -F /tmp/cfg"), " ")
	want := "ssh -F /tmp/cfg -o BatchMode=yes -o ConnectTimeout=15 -T -l root -p 2222 -- remote docker system dial-stdio"
	if got != want {
		t.Errorf("Command =\n%s\nwant\n%s", got, want)
	}
	if got := strings.Join(Target{Host: "h"}.Command("ssh"), " "); strings.Contains(got, "-l") || strings.Contains(got, "-p ") {
		t.Errorf("user/port added without being set: %s", got)
	}
}

// fakeSSH writes a script standing in for ssh and returns its path.
func fakeSSH(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConnRoundTrip(t *testing.T) {
	// "cat" echoes whatever the client sends, like a tunnel would relay it.
	c, err := Start(context.Background(), []string{fakeSSH(t, "exec cat\n")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("read %q, %v", buf, err)
	}
	if err := c.Close(); err != nil {
		t.Error(err)
	}
}

func TestConnReportsSSHErrors(t *testing.T) {
	script := fakeSSH(t, "echo 'deploy@10.0.0.5: Permission denied (publickey).' >&2\nexit 255\n")
	c, err := Start(context.Background(), []string{script})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("GET /_ping HTTP/1.1\r\n\r\n"))
	_, err = io.ReadAll(c)
	if err == nil || !strings.Contains(err.Error(), "Permission denied (publickey)") {
		t.Errorf("err = %v", err)
	}
}

func TestStartMissingBinary(t *testing.T) {
	if _, err := Start(context.Background(), []string{"/nonexistent/ssh"}); err == nil {
		t.Error("expected error")
	}
}
