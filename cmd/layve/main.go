// Command layve inspects Docker images layer by layer and cleans up what is
// buried in them: hidden file versions, leftovers and secrets.
//
// Set LAYVE_TRACE=<file> to write startup timings to that file.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AOzhogin/layve/internal/adapter/docker"
	"github.com/AOzhogin/layve/internal/adapter/hub"
	"github.com/AOzhogin/layve/internal/cli"
	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/pkg/dockerctx"
	"github.com/AOzhogin/layve/internal/ui"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	if len(os.Args) > 1 && cli.IsCommand(os.Args[1]) {
		conn := connector{store: dockerctx.NewStore(dockerctx.ConfigDir(nil), nil)}
		os.Exit(cli.Run(os.Args[1:], conn.cli, os.Stdout, os.Stderr))
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "layve:", err)
		os.Exit(1)
	}
}

func run() error {
	start := time.Now()
	flags := flag.NewFlagSet("layve", flag.ContinueOnError)
	noMark := flags.Bool("no-mark", false, `do not note "`+image.HistoryMark+`" in the history of edited layers`)
	contextName := flags.String("context", "", "Docker context to start in (default: as the docker CLI picks it)")
	showVersion := flags.Bool("version", false, "print the version and exit")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprint(out, `layve washes out what is buried in Docker image layers: hidden file versions,
leftovers and secrets.

Usage:
  layve [flags]                  interactive UI
  layve COMMAND IMAGE [flags]    for scripts and CI; "layve COMMAND -h" for details

Commands:
`+cli.Commands()+`
Flags:
`)
		flags.PrintDefaults()
		fmt.Fprint(out, `
Environment:
  DOCKER_CONTEXT, DOCKER_HOST, DOCKER_CONFIG       Docker daemon to use, as for the docker CLI
  DOCKER_TLS_VERIFY, DOCKER_CERT_PATH              TLS for DOCKER_HOST
  LAYVE_SSH_COMMAND                               ssh client for ssh:// contexts (default ssh)
  VISUAL, EDITOR                                   editor for text files (default vi)
  LAYVE_TRACE=<file>                              write timing diagnostics to <file>

See https://github.com/AOzhogin/layve for keys and examples.
`)
	}
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Println("layve", version)
		return nil
	}

	log, closeLog, err := traceLogger(os.Getenv("LAYVE_TRACE"))
	if err != nil {
		return err
	}
	defer closeLog()
	log.Debug("process start", "term", os.Getenv("TERM"), "term_program", os.Getenv("TERM_PROGRAM"))

	conn := connector{store: dockerctx.NewStore(dockerctx.ConfigDir(nil), nil)}
	current := conn.store.Current(*contextName)
	svc, err := conn.Connect(current)
	if err != nil {
		return err
	}
	log.Debug("docker client ready", "context", current, "since_start", time.Since(start))

	app := ui.NewApp(svc, log, ui.Options{MarkHistory: !*noMark, Connector: conn, Context: current, Hub: hub.New(nil)})
	defer app.Close()
	_, err = tea.NewProgram(app).Run()
	log.Debug("program exit", "since_start", time.Since(start), "err", err)
	return err
}

// service adapts the Docker adapter to ui.ImageService; Go has no
// covariant return types, so Inspect is rewrapped.
type service struct {
	*docker.Images
}

func (s service) Inspect(ctx context.Context, id image.ID, progress func(int64)) (ui.Inspection, error) {
	in, err := s.Images.Inspect(ctx, id, progress)
	if err != nil {
		return nil, err
	}
	return in, nil
}

// connector opens Docker contexts from the docker CLI's store.
type connector struct {
	store dockerctx.Store
}

func (c connector) Endpoints() ([]ui.Endpoint, error) {
	contexts, err := c.store.List()
	if err != nil {
		return nil, err
	}
	out := make([]ui.Endpoint, len(contexts))
	for i, x := range contexts {
		out[i] = ui.Endpoint{Name: x.Name, Description: x.Description, Address: x.Address()}
	}
	return out, nil
}

func (c connector) Connect(name string) (ui.ImageService, error) {
	ctx, err := c.store.Get(name)
	if err != nil {
		return nil, err
	}
	images, err := docker.NewImagesFor(ctx)
	if err != nil {
		return nil, err
	}
	return service{images}, nil
}

// cli connects for the non-interactive commands; an empty name picks the
// current context.
func (c connector) cli(name string) (cli.Service, error) {
	if name == "" {
		name = c.store.Current("")
	}
	ctx, err := c.store.Get(name)
	if err != nil {
		return nil, err
	}
	images, err := docker.NewImagesFor(ctx)
	if err != nil {
		return nil, err
	}
	return cliService{images}, nil
}

// cliService rewraps Inspect for cli.Service, like service does for the UI.
type cliService struct {
	*docker.Images
}

func (s cliService) Inspect(ctx context.Context, id image.ID, progress func(int64)) (cli.Inspection, error) {
	in, err := s.Images.Inspect(ctx, id, progress)
	if err != nil {
		return nil, err
	}
	return in, nil
}

func traceLogger(path string) (*slog.Logger, func(), error) {
	if path == "" {
		return slog.New(slog.NewTextHandler(io.Discard, nil)), func() {}, nil
	}
	f, err := os.Create(path) //nolint:gosec // G703: the user names the trace file
	if err != nil {
		return nil, nil, fmt.Errorf("open trace file: %w", err)
	}
	h := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), func() { _ = f.Close() }, nil
}
