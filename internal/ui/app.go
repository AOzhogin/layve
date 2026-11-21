package ui

import (
	"fmt"
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"
)

// App is the root model: the image list plus an optional layers screen.
type App struct {
	opts     Options
	svc      ImageService
	log      *slog.Logger
	images   ImagesModel
	layers   *LayersModel
	hub      *HubModel
	sessions int
	width    int
	height   int
	started  time.Time
	drawn    bool
	dataSeen bool
}

// Options configures the UI.
type Options struct {
	// Connector enables switching Docker contexts and copying images
	// between them; Context names the one svc is connected to.
	Connector Connector
	Context   string
	// Hub enables searching and pulling from Docker Hub.
	Hub Hub

	// MarkHistory is the initial state of the "edited with layve" history
	// note when writing an edited image; it can be toggled per write.
	MarkHistory bool
}

// NewApp creates the root model. log receives timing diagnostics.
func NewApp(svc ImageService, log *slog.Logger, opts Options) *App {
	return &App{
		opts:    opts,
		svc:     svc,
		log:     log,
		images:  newImagesWith(svc, log, opts),
		width:   80,
		height:  24,
		started: time.Now(),
	}
}

// Init starts loading the image list.
func (a *App) Init() tea.Cmd {
	a.log.Debug("ui init")
	return a.images.Init()
}

// Update routes messages to the active screen.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			a.closeLayers()
			return a, tea.Quit
		}
	case tea.WindowSizeMsg:
		a.log.Debug("window size", "w", msg.Width, "h", msg.Height, "since_start", time.Since(a.started))
		a.width, a.height = msg.Width, msg.Height
		var cmd tea.Cmd
		a.images, cmd = a.images.Update(msg)
		if a.layers != nil {
			a.layers.Update(msg)
		}
		if a.hub != nil {
			a.hub.Update(msg)
		}
		return a, cmd
	case imagesLoadedMsg:
		if !a.dataSeen {
			a.dataSeen = true
			a.log.Debug("images arrived", "since_start", time.Since(a.started))
		}
	case switchContextMsg:
		return a, a.switchContext(msg.name)
	case openHubMsg:
		if a.opts.Hub == nil {
			a.images.status, a.images.statusOK = "Docker Hub search is not available", false
			return a, nil
		}
		a.hub = NewHub(a.opts.Hub, a.svc, a.opts.Context, a.width, a.height)
		return a, a.hub.Init()
	case closeHubMsg:
		a.hub = nil
		if msg.pulled {
			a.images.loading = true
			return a, a.images.load()
		}
		return a, nil
	case hubReposMsg, hubTagsMsg, hubPlatformMsg, pullProgressMsg, pullDoneMsg:
		if a.hub != nil {
			return a, a.hub.Update(msg)
		}
		return a, nil
	case openLayersMsg:
		a.closeLayers()
		a.sessions++
		a.layers = NewLayers(a.svc, msg.img, a.sessions, a.width, a.height)
		a.layers.markHistory = a.opts.MarkHistory
		return a, a.layers.Init()
	case closeLayersMsg:
		a.closeLayers()
		return a, nil
	case imageWrittenMsg:
		a.closeLayers()
		a.images.status = "Saved as " + msg.ref.String()
		a.images.statusOK = true
		a.images.loading = true
		return a, a.images.load()
	case historyMsg, detailsProgressMsg, detailsMsg, layersTickMsg, writeStageMsg, writeDoneMsg, editDoneMsg:
		if a.layers != nil {
			return a, a.layers.Update(msg)
		}
		return a, nil
	}

	if _, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case a.hub != nil:
			return a, a.hub.Update(msg)
		case a.layers != nil:
			return a, a.layers.Update(msg)
		}
	}
	var cmd tea.Cmd
	a.images, cmd = a.images.Update(msg)
	return a, cmd
}

// Close stops background work and disconnects from the daemon.
func (a *App) Close() error {
	a.closeLayers()
	return a.svc.Close()
}

func (a *App) closeScreens() {
	a.closeLayers()
	a.hub = nil
}

func (a *App) closeLayers() {
	if a.layers != nil {
		a.layers.Close()
		a.layers = nil
	}
}

// View renders the active screen.
func (a *App) View() tea.View {
	if !a.drawn {
		a.drawn = true
		a.log.Debug("first frame", "since_start", time.Since(a.started))
	}
	content := a.images.View()
	switch {
	case a.hub != nil:
		content = a.hub.View()
	case a.layers != nil:
		content = a.layers.View()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "layve"
	return v
}

func newImagesWith(svc ImageService, log *slog.Logger, opts Options) ImagesModel {
	m := NewImages(svc, log)
	m.conn, m.context = opts.Connector, opts.Context
	return m
}

// switchContext connects to another daemon; everything after happens there.
func (a *App) switchContext(name string) tea.Cmd {
	svc, err := a.opts.Connector.Connect(name)
	if err != nil {
		a.images.status, a.images.statusOK = fmt.Sprintf("context %s: %v", name, err), false
		return nil
	}
	a.closeScreens()
	_ = a.svc.Close()
	a.svc = svc
	a.opts.Context = name
	m := newImagesWith(svc, a.log, a.opts)
	m, _ = m.Update(tea.WindowSizeMsg{Width: a.width, Height: a.height})
	a.images = m
	a.log.Debug("switched context", "name", name)
	return a.images.Init()
}
