// Package cli implements layve's non-interactive commands, meant for CI
// pipelines: inspect, check and fix.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/AOzhogin/layve/internal/domain/image"
)

// Exit codes.
const (
	ExitOK          = 0
	ExitError       = 1
	ExitCheckFailed = 2
)

// Service is what the commands need from a Docker daemon.
type Service interface {
	Find(ctx context.Context, name string) (image.Image, error)
	Inspect(ctx context.Context, id image.ID, progress func(read int64)) (Inspection, error)
	Write(ctx context.Context, id image.ID, plan image.EditPlan, ref image.Reference, opts image.WriteOptions, progress func(stage string)) error
	Close() error
}

// Inspection previews an image with edits applied.
type Inspection interface {
	Details(plan image.EditPlan) ([]image.LayerDetail, error)
}

// Connect opens a Docker context; an empty name picks the current one.
type Connect func(contextName string) (Service, error)

// timeout bounds one command; big images on slow remotes take a while.
const timeout = time.Hour

type command struct {
	name    string
	summary string
	run     func(e *env, args []string) int
}

var commands = []command{
	{"inspect", "show layers, sizes and wasted space of an image", runInspect},
	{"check", "fail when an image wastes more space than allowed", runCheck},
	{"fix", "remove wasted space and secrets, merge or drop layers", runFix},
}

// IsCommand reports whether name is a CLI command rather than a TUI flag.
func IsCommand(name string) bool {
	return slices.ContainsFunc(commands, func(c command) bool { return c.name == name })
}

// Commands describes the commands for the main usage text.
func Commands() string {
	var b strings.Builder
	for _, c := range commands {
		fmt.Fprintf(&b, "  %-8s %s\n", c.name, c.summary)
	}
	return b.String()
}

type env struct {
	connect        Connect
	stdout, stderr io.Writer
}

// Run executes a command; args[0] is its name. It returns the exit code.
func Run(args []string, connect Connect, stdout, stderr io.Writer) int {
	e := &env{connect: connect, stdout: stdout, stderr: stderr}
	for _, c := range commands {
		if c.name == args[0] {
			return c.run(e, args[1:])
		}
	}
	fmt.Fprintf(stderr, "layve: unknown command %q\n", args[0])
	return ExitError
}

func (e *env) fail(err error) int {
	fmt.Fprintln(e.stderr, "layve:", err)
	return ExitError
}

// common holds flags shared by all commands.
type common struct {
	context string
	json    bool
}

func newFlags(name, usage string, e *env, c *common) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.StringVar(&c.context, "context", "", "Docker context to use (default: as the docker CLI picks it)")
	fs.BoolVar(&c.json, "json", false, "print the result as JSON")
	fs.Usage = func() {
		fmt.Fprintf(e.stderr, "Usage:\n  layve %s\n\nFlags:\n", usage)
		fs.PrintDefaults()
	}
	return fs
}

// parse accepts flags before and after positional arguments, so both
// "fix --wasted app:1" and "fix app:1 --wasted" work.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// parseImageArgs parses flags and expects exactly one image argument.
func parseImageArgs(fs *flag.FlagSet, args []string) (string, int, bool) {
	pos, err := parse(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return "", ExitOK, false
	}
	if err != nil {
		return "", ExitError, false
	}
	if len(pos) != 1 {
		fmt.Fprintln(fs.Output(), "layve: expected exactly one image")
		fs.Usage()
		return "", ExitError, false
	}
	return pos[0], 0, true
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// load connects, finds the image and reads it.
func (e *env) load(ctx context.Context, c common, name string) (Service, image.Image, Inspection, error) {
	svc, err := e.connect(c.context)
	if err != nil {
		return nil, image.Image{}, nil, err
	}
	img, err := svc.Find(ctx, name)
	if err != nil {
		_ = svc.Close()
		return nil, image.Image{}, nil, err
	}
	in, err := svc.Inspect(ctx, img.ID(), nil)
	if err != nil {
		_ = svc.Close()
		return nil, image.Image{}, nil, err
	}
	return svc, img, in, nil
}
