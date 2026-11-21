package ui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/pkg/humanize"
)

// copyTimeout bounds a copy between daemons; images can be large and links slow.
const copyTimeout = 2 * time.Hour

type pickPurpose int

const (
	pickSwitch pickPurpose = iota
	pickCopy
)

type (
	contextsLoadedMsg struct {
		purpose   pickPurpose
		endpoints []Endpoint
		err       error
	}
	// switchContextMsg asks the app to connect to another context.
	switchContextMsg    struct{ name string }
	transferProgressMsg struct{ read int64 }
	transferDoneMsg     struct {
		bytes int64
		err   error
	}
)

// contextPicker is the list shown by "c" and "y".
type contextPicker struct {
	purpose   pickPurpose
	endpoints []Endpoint
	cursor    int
	pending   []row // images to copy
}

// transfer is a running copy, save or load: a byte stream with progress.
type transfer struct {
	active  bool
	label   string // "Copying 2 image(s) to prod"
	done    string // "Copied 2 image(s) to prod"
	failed  string // "copy to prod failed"
	reload  bool   // refresh the list afterwards
	read    int64
	total   int64
	started time.Time
}

// runTransfer starts fn in the background, reporting progress; only one
// transfer runs at a time.
func (m ImagesModel) runTransfer(t transfer, fn func(progress func(int64)) (int64, error)) (ImagesModel, tea.Cmd) {
	t.active, t.started = true, time.Now()
	m.transfer = t
	events := m.events
	go func() {
		bytes, err := fn(func(read int64) {
			select {
			case events <- transferProgressMsg{read: read}:
			default:
			}
		})
		events <- transferDoneMsg{bytes: bytes, err: err}
	}()
	return m, m.waitEvent()
}

// busyTransfer reports a running transfer to the user.
func (m *ImagesModel) busyTransfer() bool {
	if m.transfer.active {
		m.status, m.statusOK = "wait for the current transfer to finish", false
		return true
	}
	return false
}

func (m ImagesModel) openPicker(purpose pickPurpose) (ImagesModel, tea.Cmd) {
	if m.conn == nil {
		m.status, m.statusOK = "Docker contexts are not available", false
		return m, nil
	}
	if m.transfer.active {
		m.status, m.statusOK = "wait for the current transfer to finish", false
		return m, nil
	}
	m.picker = contextPicker{purpose: purpose}
	if purpose == pickCopy {
		m.picker.pending = m.targets()
	}
	conn := m.conn
	return m, func() tea.Msg {
		eps, err := conn.Endpoints()
		return contextsLoadedMsg{purpose: purpose, endpoints: eps, err: err}
	}
}

func (m ImagesModel) updateContexts(msg tea.Msg) (ImagesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case contextsLoadedMsg:
		if msg.err != nil {
			m.status, m.statusOK = "contexts: "+msg.err.Error(), false
			return m, nil
		}
		eps := msg.endpoints
		if msg.purpose == pickCopy {
			eps = slices.DeleteFunc(slices.Clone(eps), func(e Endpoint) bool { return e.Name == m.context })
			if len(eps) == 0 {
				m.status, m.statusOK = "no other Docker context to copy to; add one with \"docker context create\"", false
				return m, nil
			}
		}
		m.picker.endpoints = eps
		m.picker.cursor = 0
		for i, e := range eps {
			if e.Name == m.context {
				m.picker.cursor = i
			}
		}
		m.mode = modePickContext
		return m, nil
	case transferProgressMsg:
		m.transfer.read = msg.read
		return m, m.waitEvent()
	case transferDoneMsg:
		t := m.transfer
		m.transfer.active = false
		if msg.err != nil {
			m.status, m.statusOK = fmt.Sprintf("%s: %v", t.failed, msg.err), false
			return m, nil
		}
		m.status = fmt.Sprintf("%s (%s in %s)", t.done, humanize.Bytes(msg.bytes), time.Since(t.started).Round(100*time.Millisecond))
		m.statusOK = true
		if t.reload {
			m.loading = true
			return m, m.load()
		}
		return m, nil
	}
	return m, nil
}

func (m ImagesModel) updatePicker(msg tea.KeyPressMsg) (ImagesModel, tea.Cmd) {
	p := &m.picker
	switch msg.String() {
	case "up", "k":
		p.cursor = max(p.cursor-1, 0)
	case "down", "j":
		p.cursor = min(p.cursor+1, len(p.endpoints)-1)
	case "esc", "q":
		m.mode = modeList
	case "enter":
		m.mode = modeList
		target := p.endpoints[p.cursor].Name
		if p.purpose == pickSwitch {
			if target == m.context {
				return m, nil
			}
			return m, func() tea.Msg { return switchContextMsg{name: target} }
		}
		return m.startCopy(target, p.pending)
	}
	return m, nil
}

// startCopy streams the images out of this daemon straight into the target
// one: no temporary files, tags preserved.
func (m ImagesModel) startCopy(target string, rows []row) (ImagesModel, tea.Cmd) {
	targets, total := exportTargets(rows)
	m.selected = map[string]bool{}
	m.refreshTable()
	n := len(targets)
	src, conn := m.svc, m.conn
	return m.runTransfer(transfer{
		label:  fmt.Sprintf("Copying %d image(s) to %s", n, target),
		done:   fmt.Sprintf("Copied %d image(s) to %s", n, target),
		failed: "copy to " + target + " failed",
		total:  total,
	}, func(progress func(int64)) (int64, error) {
		return copyImages(src, conn, target, targets, progress)
	})
}

// exportTargets returns what to export for rows and the approximate size.
func exportTargets(rows []row) ([]image.Target, int64) {
	var targets []image.Target
	var total int64
	seen := map[image.ID]bool{}
	for _, r := range rows {
		targets = append(targets, r.target)
		if !seen[r.img.ID()] {
			seen[r.img.ID()] = true
			total += r.img.Size().Bytes()
		}
	}
	return targets, total
}

func copyImages(src ImageService, conn Connector, target string, targets []image.Target, progress func(int64)) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), copyTimeout)
	defer cancel()
	dst, err := conn.Connect(target)
	if err != nil {
		return 0, err
	}
	defer dst.Close()
	rc, err := src.Export(ctx, targets)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	counter := &progressReader{r: rc, report: progress}
	err = dst.Import(ctx, counter)
	return counter.read, err
}

type progressReader struct {
	r      io.Reader
	read   int64
	report func(int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	p.report(p.read)
	return n, err
}

func (m ImagesModel) waitEvent() tea.Cmd {
	events := m.events
	return func() tea.Msg { return <-events }
}

func (m ImagesModel) pickerView() string {
	p := m.picker
	title := "Switch Docker context"
	if p.purpose == pickCopy {
		title = fmt.Sprintf("Copy %d image(s) from %s to…", len(p.pending), m.context)
	}
	lines := []string{boldStyle.Render(title)}
	nameW, addrW := 0, 0
	for _, e := range p.endpoints {
		nameW = max(nameW, len(e.Name))
		addrW = max(addrW, len(e.Address))
	}
	for i, e := range p.endpoints {
		mark := "  "
		if e.Name == m.context {
			mark = "● "
		}
		line := fmt.Sprintf("%s%-*s  %-*s", mark, nameW, e.Name, addrW, e.Address)
		if e.Description != "" {
			line += "  " + subtleStyle.Render(e.Description)
		}
		line = ansi.Truncate(line, 100, "…")
		if i == p.cursor {
			line = cursorStyle.Render(line)
		}
		lines = append(lines, line)
	}
	return dialogStyle.Render(strings.Join(lines, "\n"))
}

func (m ImagesModel) transferView() string {
	t := m.transfer
	text := fmt.Sprintf("%s… %s", t.label, humanize.Bytes(t.read))
	if t.total > 0 && t.read < t.total {
		text += fmt.Sprintf(" of ~%s", humanize.Bytes(t.total))
	}
	text += " · " + time.Since(t.started).Round(time.Second).String()
	return markStyle.Render(" " + text)
}
