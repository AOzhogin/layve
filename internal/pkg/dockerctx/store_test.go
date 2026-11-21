package dockerctx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeContext(t *testing.T, dir, name, host, desc string, tls bool) {
	t.Helper()
	id := digest(name)
	metaDir := filepath.Join(dir, "contexts", "meta", id)
	if err := os.MkdirAll(metaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"Name":"` + name + `","Metadata":{"Description":"` + desc + `"},"Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":false}}}`
	if err := os.WriteFile(filepath.Join(metaDir, "meta.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if tls {
		tlsDir := filepath.Join(dir, "contexts", "tls", id, "docker")
		_ = os.MkdirAll(tlsDir, 0o700)
		_ = os.WriteFile(filepath.Join(tlsDir, "ca.pem"), []byte("x"), 0o600)
	}
}

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestListAndGet(t *testing.T) {
	dir := t.TempDir()
	writeContext(t, dir, "prod", "ssh://deploy@10.0.0.5", "Production", false)
	writeContext(t, dir, "build", "tcp://build:2376", "", true)
	s := NewStore(dir, env(nil))

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range list {
		names = append(names, c.Name)
	}
	if len(names) != 3 || names[0] != "default" || names[1] != "build" || names[2] != "prod" {
		t.Fatalf("names = %v", names)
	}
	prod, err := s.Get("prod")
	if err != nil || prod.Host != "ssh://deploy@10.0.0.5" || prod.Description != "Production" || prod.TLSDir != "" {
		t.Errorf("prod = %+v, %v", prod, err)
	}
	build, _ := s.Get("build")
	if build.TLSDir == "" {
		t.Error("TLS dir not found")
	}
	if _, err := s.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if d, _ := s.Get("default"); d.Host != "" {
		t.Errorf("default host = %q", d.Host)
	}
}

func TestListWithoutStore(t *testing.T) {
	list, err := NewStore(t.TempDir(), env(nil)).List()
	if err != nil || len(list) != 1 || list[0].Name != DefaultName {
		t.Errorf("list = %+v, %v", list, err)
	}
}

func TestCurrent(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"currentContext":"prod"}`), 0o600)
	cases := []struct {
		explicit string
		env      map[string]string
		want     string
	}{
		{"", nil, "prod"},
		{"build", map[string]string{"DOCKER_CONTEXT": "x"}, "build"},
		{"", map[string]string{"DOCKER_CONTEXT": "x", "DOCKER_HOST": "tcp://h:1"}, "x"},
		{"", map[string]string{"DOCKER_HOST": "tcp://h:1"}, "default"},
	}
	for _, c := range cases {
		if got := NewStore(dir, env(c.env)).Current(c.explicit); got != c.want {
			t.Errorf("Current(%q, %v) = %q, want %q", c.explicit, c.env, got, c.want)
		}
	}
	if got := NewStore(t.TempDir(), env(nil)).Current(""); got != "default" {
		t.Errorf("no config: %q", got)
	}
}

func TestDigestMatchesDockerCLI(t *testing.T) {
	// The docker CLI stores "desktop-linux" under this directory name.
	if got := digest("desktop-linux"); got != "fe9c6bd7a66301f49ca9b6a70b217107cd1284598bfc254700c989b916da791e" {
		t.Errorf("digest = %s", got)
	}
}
