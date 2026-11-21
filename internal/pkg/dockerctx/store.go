// Package dockerctx reads Docker CLI contexts (the "docker context" store)
// and resolves which one is current, the same way the docker CLI does.
package dockerctx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// DefaultName is the built-in context backed by DOCKER_HOST or the local socket.
const DefaultName = "default"

// DefaultHost is the daemon address of the default context on Unix.
const DefaultHost = "unix:///var/run/docker.sock"

// Context is a named Docker endpoint.
type Context struct {
	Name        string
	Description string
	// Host is the daemon address: unix://, tcp://, ssh:// or npipe://.
	// Empty for the default context, which follows the DOCKER_* variables.
	Host          string
	SkipTLSVerify bool
	// TLSDir holds ca.pem, cert.pem and key.pem when the context has TLS
	// material; empty otherwise.
	TLSDir string
}

// Store is a Docker CLI configuration directory.
type Store struct {
	dir    string
	getenv func(string) string
}

// ErrNotFound is returned for unknown context names.
var ErrNotFound = errors.New("context not found")

// NewStore reads contexts from a Docker configuration directory. getenv
// resolves DOCKER_HOST for the default context; nil means os.Getenv.
func NewStore(dir string, getenv func(string) string) Store {
	if getenv == nil {
		getenv = os.Getenv
	}
	return Store{dir: dir, getenv: getenv}
}

// ConfigDir returns $DOCKER_CONFIG or ~/.docker.
func ConfigDir(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if d := getenv("DOCKER_CONFIG"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".docker"
	}
	return filepath.Join(home, ".docker")
}

type meta struct {
	Name     string `json:"Name"`
	Metadata struct {
		Description string `json:"Description"`
	} `json:"Metadata"`
	Endpoints map[string]struct {
		Host          string `json:"Host"`
		SkipTLSVerify bool   `json:"SkipTLSVerify"`
	} `json:"Endpoints"`
}

func digest(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

// List returns the default context followed by the stored ones by name.
func (s Store) List() ([]Context, error) {
	out := []Context{s.defaultContext()}
	dirs, err := os.ReadDir(filepath.Join(s.dir, "contexts", "meta"))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read contexts: %w", err)
	}
	var stored []Context
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		c, err := s.load(d.Name())
		if err != nil {
			return nil, err
		}
		if c.Name != DefaultName {
			stored = append(stored, c)
		}
	}
	sort.Slice(stored, func(i, j int) bool { return stored[i].Name < stored[j].Name })
	return append(out, stored...), nil
}

// Get returns a context by name.
func (s Store) Get(name string) (Context, error) {
	if name == DefaultName {
		return s.defaultContext(), nil
	}
	c, err := s.load(digest(name))
	if errors.Is(err, os.ErrNotExist) {
		return Context{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return Context{}, err
	}
	return c, nil
}

func (s Store) load(id string) (Context, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "contexts", "meta", id, "meta.json"))
	if err != nil {
		return Context{}, err
	}
	var m meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Context{}, fmt.Errorf("parse context %s: %w", id, err)
	}
	c := Context{Name: m.Name, Description: m.Metadata.Description}
	if ep, ok := m.Endpoints["docker"]; ok {
		c.Host, c.SkipTLSVerify = ep.Host, ep.SkipTLSVerify
	}
	tls := filepath.Join(s.dir, "contexts", "tls", id, "docker")
	if _, err := os.Stat(filepath.Join(tls, "ca.pem")); err == nil {
		c.TLSDir = tls
	}
	return c, nil
}

func (s Store) defaultContext() Context {
	c := Context{Name: DefaultName, Description: "Current DOCKER_HOST based configuration"}
	if h := s.getenv("DOCKER_HOST"); h != "" {
		c.Description = "DOCKER_HOST=" + h
	}
	return c
}

// Current resolves the context to use, like the docker CLI: an explicit
// name (the --context flag), then DOCKER_CONTEXT, then DOCKER_HOST (which
// selects the default context), then currentContext from config.json.
func (s Store) Current(explicit string) string {
	switch {
	case explicit != "":
		return explicit
	case s.getenv("DOCKER_CONTEXT") != "":
		return s.getenv("DOCKER_CONTEXT")
	case s.getenv("DOCKER_HOST") != "":
		return DefaultName
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "config.json"))
	if err != nil {
		return DefaultName
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if json.Unmarshal(data, &cfg) != nil || cfg.CurrentContext == "" {
		return DefaultName
	}
	return cfg.CurrentContext
}

// Address returns the host shown to users.
func (c Context) Address() string {
	if c.Host != "" {
		return c.Host
	}
	return "DOCKER_HOST or " + DefaultHost
}
