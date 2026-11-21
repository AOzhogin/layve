package ui

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/pkg/humanize"
)

const (
	listTimeout = 10 * time.Second
	opTimeout   = 2 * time.Minute
)

type mode int

const (
	modeList mode = iota
	modeConfirmDelete
	modeRetag
	modeBusy
	modePickContext
	modeSavePath
	modeLoadPath
)

type imagesLoadedMsg struct {
	images  []image.Image
	err     error
	elapsed time.Duration
}

type opDoneMsg struct{ result opResult }

// openHubMsg asks the app to show the Docker Hub screen.
type openHubMsg struct{}

// openLayersMsg asks the app to show the layers of an image.
type openLayersMsg struct{ img image.Image }

// ImagesModel is the main screen listing local images.
type ImagesModel struct {
	svc      ImageService
	conn     Connector
	context  string
	picker   contextPicker
	events   chan tea.Msg
	transfer transfer
	log      *slog.Logger
	table    table.Model
	input    textinput.Model
	rows     []row
	selected map[string]bool
	mode     mode
	force    bool
	pending  []row
	loading  bool
	loaded   bool
	elapsed  time.Duration
	err      error
	status   string
	statusOK bool
	height   int
	now      func() time.Time
}

// NewImages creates the local images screen.
func NewImages(svc ImageService, log *slog.Logger) ImagesModel {
	t := table.New(
		table.WithColumns(columns(80)),
		table.WithFocused(true),
		table.WithHeight(10),
		table.WithKeyMap(tableKeys()),
	)
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true).
		BorderStyle(lipgloss.NormalBorder()).BorderBottom(true)
	styles.Selected = styles.Selected.Foreground(colorSelected).Background(colorCursorBg)
	t.SetStyles(styles)

	in := newInput()
	in.Prompt = "> "

	return ImagesModel{
		svc:      svc,
		log:      log,
		table:    t,
		input:    in,
		selected: map[string]bool{},
		events:   make(chan tea.Msg, 1),
		loading:  true,
		height:   24,
		now:      time.Now,
	}
}

// tableKeys frees space, d and u for our own actions.
func tableKeys() table.KeyMap {
	km := table.DefaultKeyMap()
	km.PageDown = key.NewBinding(key.WithKeys("pgdown", "f"))
	km.PageUp = key.NewBinding(key.WithKeys("pgup", "b"))
	km.HalfPageDown = key.NewBinding(key.WithKeys("ctrl+d"))
	km.HalfPageUp = key.NewBinding(key.WithKeys("ctrl+u"))
	return km
}

func columns(width int) []table.Column {
	fixed := []table.Column{
		{Title: " ", Width: 1},
		{Title: "TAG", Width: 20},
		{Title: "IMAGE ID", Width: 12},
		{Title: "CREATED", Width: 16},
		{Title: "SIZE", Width: 8},
	}
	used := 0
	for _, c := range fixed {
		used += c.Width + 2 // cell padding
	}
	repo := max(width-used-2, 20)
	return slices.Insert(fixed, 1, table.Column{Title: "REPOSITORY", Width: repo})
}

// Init starts loading images.
func (m ImagesModel) Init() tea.Cmd { return m.load() }

func (m ImagesModel) load() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
		defer cancel()
		start := time.Now()
		images, err := m.svc.List(ctx)
		elapsed := time.Since(start)
		m.log.Debug("list images", "count", len(images), "elapsed", elapsed, "err", err)
		return imagesLoadedMsg{images: images, err: err, elapsed: elapsed}
	}
}

// Update handles input and loaded data.
func (m ImagesModel) Update(msg tea.Msg) (ImagesModel, tea.Cmd) {
	m, cmd := m.update(msg)
	m.resizeTable()
	return m, cmd
}

func (m ImagesModel) update(msg tea.Msg) (ImagesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height
		m.table.SetColumns(columns(msg.Width))
		m.table.SetWidth(msg.Width)
		m.input.SetWidth(min(msg.Width-10, 70))
		return m, nil
	case imagesLoadedMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.loaded = true
			m.elapsed = msg.elapsed
			m.rows = buildRows(msg.images)
			m.pruneSelection()
			m.refreshTable()
		}
		return m, nil
	case contextsLoadedMsg, transferProgressMsg, transferDoneMsg:
		return m.updateContexts(msg)
	case opDoneMsg:
		m.mode = modeList
		m.status = msg.result.message()
		m.statusOK = len(msg.result.errs) == 0
		m.selected = map[string]bool{}
		m.loading = true
		return m, m.load()
	case tea.KeyPressMsg:
		switch m.mode {
		case modeConfirmDelete:
			return m.updateConfirm(msg)
		case modeRetag:
			return m.updateRetag(msg)
		case modeBusy:
			return m, nil
		case modePickContext:
			return m.updatePicker(msg)
		case modeSavePath, modeLoadPath:
			return m.updatePathInput(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

func (m ImagesModel) updateList(msg tea.KeyPressMsg) (ImagesModel, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "r":
		m.loading = true
		m.status = ""
		return m, m.load()
	case "space":
		if r, ok := m.current(); ok {
			m.toggle(r.key())
			m.table.MoveDown(1)
			m.refreshTable()
		}
		return m, nil
	case "a":
		m.toggleAll()
		m.refreshTable()
		return m, nil
	case "esc":
		m.selected = map[string]bool{}
		m.refreshTable()
		return m, nil
	case "d", "D":
		if targets := m.targets(); len(targets) > 0 {
			m.pending = targets
			m.force = msg.String() == "D"
			m.mode = modeConfirmDelete
		}
		return m, nil
	case "t":
		targets := m.targets()
		if len(targets) == 0 {
			return m, nil
		}
		m.pending = targets
		m.mode = modeRetag
		m.input.SetValue("")
		m.input.Placeholder = "new tag, e.g. v2"
		if len(targets) == 1 {
			m.input.Placeholder = "repository:tag"
			if ref, ok := targets[0].target.Reference(); ok {
				m.input.SetValue(ref.String())
			}
		}
		m.input.CursorEnd()
		return m, m.input.Focus()
	case "c":
		return m.openPicker(pickSwitch)
	case "/":
		return m, func() tea.Msg { return openHubMsg{} }
	case "s":
		return m.askSavePath()
	case "l":
		return m.askLoadPath()
	case "y":
		if len(m.targets()) > 0 {
			return m.openPicker(pickCopy)
		}
		return m, nil
	case "enter":
		if r, ok := m.current(); ok {
			return m, func() tea.Msg { return openLayersMsg{img: r.img} }
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m ImagesModel) updateConfirm(msg tea.KeyPressMsg) (ImagesModel, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		rows, force, svc := m.pending, m.force, m.svc
		m.mode = modeBusy
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			return opDoneMsg{result: runRemove(ctx, svc, rows, force)}
		}
	case "n", "esc", "q":
		m.mode = modeList
	}
	return m, nil
}

func (m ImagesModel) updateRetag(msg tea.KeyPressMsg) (ImagesModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	case "enter":
		steps, err := planRetag(m.pending, m.input.Value())
		if err != nil {
			m.status, m.statusOK = err.Error(), false
			return m, nil
		}
		m.input.Blur()
		m.mode = modeBusy
		svc := m.svc
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			return opDoneMsg{result: runRetag(ctx, svc, steps)}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// targets returns the selected rows in table order, or the row under the
// cursor when nothing is selected.
func (m ImagesModel) targets() []row {
	var out []row
	for _, r := range m.rows {
		if m.selected[r.key()] {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		if r, ok := m.current(); ok {
			out = append(out, r)
		}
	}
	return out
}

func (m ImagesModel) current() (row, bool) {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.rows) {
		return row{}, false
	}
	return m.rows[i], true
}

func (m *ImagesModel) toggle(k string) {
	if m.selected[k] {
		delete(m.selected, k)
	} else {
		m.selected[k] = true
	}
}

func (m *ImagesModel) toggleAll() {
	if len(m.selected) == len(m.rows) {
		m.selected = map[string]bool{}
		return
	}
	for _, r := range m.rows {
		m.selected[r.key()] = true
	}
}

func (m *ImagesModel) pruneSelection() {
	present := map[string]bool{}
	for _, r := range m.rows {
		present[r.key()] = true
	}
	for k := range m.selected {
		if !present[k] {
			delete(m.selected, k)
		}
	}
}

func (m *ImagesModel) refreshTable() {
	m.table.SetRows(tableRows(m.rows, m.selected, m.now()))
}

// resizeTable gives the table whatever the title, help, status line and any
// open dialog leave free.
func (m *ImagesModel) resizeTable() {
	reserved := 2 // title + help
	if m.status != "" {
		reserved++
	}
	switch m.mode {
	case modeConfirmDelete:
		reserved += lipgloss.Height(m.confirmView())
	case modeRetag:
		reserved += lipgloss.Height(m.retagView())
	case modePickContext:
		reserved += lipgloss.Height(m.pickerView())
	case modeSavePath, modeLoadPath:
		reserved += lipgloss.Height(m.pathView())
	}
	if m.transfer.active {
		reserved++
	}
	m.table.SetHeight(max(m.height-reserved, 3))
}

// buildRows yields one row per tag, newest images first; untagged images get
// a single row addressed by ID.
func buildRows(images []image.Image) []row {
	images = slices.Clone(images)
	slices.SortStableFunc(images, func(a, b image.Image) int {
		return b.Created().Compare(a.Created())
	})
	var out []row
	for _, img := range images {
		if img.Dangling() {
			out = append(out, row{img: img, target: image.IDTarget(img.ID())})
			continue
		}
		for _, ref := range img.References() {
			out = append(out, row{img: img, target: image.ReferenceTarget(ref)})
		}
	}
	return out
}

func tableRows(rows []row, selected map[string]bool, now time.Time) []table.Row {
	out := make([]table.Row, 0, len(rows))
	for _, r := range rows {
		mark := " "
		if selected[r.key()] {
			mark = "●"
		}
		repo, tag := "<none>", "<none>"
		if ref, ok := r.target.Reference(); ok {
			repo, tag = ref.Repository(), ref.Tag()
		}
		out = append(out, table.Row{
			mark, repo, tag, r.img.ID().Short(),
			humanize.Since(r.img.Created(), now), r.img.Size().String(),
		})
	}
	return out
}

// View renders the screen.
func (m ImagesModel) View() string {
	header := titleStyle.Render("Local images")
	if m.context != "" {
		header = titleStyle.Render("Images @ " + m.context)
	}
	if m.loaded {
		header += subtleStyle.Render(fmt.Sprintf("%d rows · loaded in %s", len(m.rows), m.elapsed.Round(time.Millisecond)))
	}
	if n := len(m.selected); n > 0 {
		header += markStyle.Render(fmt.Sprintf("  ● %d selected", n))
	}
	if m.loading && m.loaded {
		header += subtleStyle.Render("  refreshing…")
	}

	var body string
	switch {
	case m.err != nil:
		body = errStyle.Render("Error: " + m.err.Error())
	case !m.loaded:
		body = helpStyle.Render("Loading…")
	default:
		body = m.table.View()
	}

	parts := []string{header, body}
	switch m.mode {
	case modeConfirmDelete:
		parts = append(parts, m.confirmView())
	case modeRetag:
		parts = append(parts, m.retagView())
	case modePickContext:
		parts = append(parts, m.pickerView())
	case modeSavePath, modeLoadPath:
		parts = append(parts, m.pathView())
	}
	if m.transfer.active {
		parts = append(parts, m.transferView())
	}
	if m.status != "" {
		style := errStyle
		if m.statusOK {
			style = okStyle
		}
		parts = append(parts, style.Render(m.status))
	}
	parts = append(parts, helpStyle.Render(m.help()))
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m ImagesModel) confirmView() string {
	verb := "Remove"
	if m.force {
		verb = "Force remove"
	}
	names := make([]string, 0, len(m.pending))
	for _, r := range m.pending {
		names = append(names, r.target.Display())
	}
	const maxShown = 8
	if len(names) > maxShown {
		names = append(names[:maxShown], fmt.Sprintf("… and %d more", len(names)-maxShown))
	}
	text := fmt.Sprintf("%s %d image(s)?\n%s\n\n[y] yes  [n] no", verb, len(m.pending), strings.Join(names, "\n"))
	return dialogStyle.Render(text)
}

func (m ImagesModel) retagView() string {
	title := "New name for " + m.pending[0].target.Display()
	if len(m.pending) > 1 {
		title = fmt.Sprintf("New tag for %d images (repository stays the same)", len(m.pending))
	}
	return dialogStyle.Render(title + "\n" + m.input.View())
}

func (m ImagesModel) help() string {
	switch m.mode {
	case modeConfirmDelete:
		return "y confirm • n cancel"
	case modeRetag:
		return "enter apply • esc cancel"
	case modeBusy:
		return "working…"
	case modePickContext:
		return "↑/↓ choose • enter confirm • esc cancel"
	case modeSavePath, modeLoadPath:
		return "enter confirm • esc cancel"
	}
	return "space select • a all • enter layers • / Docker Hub • d delete • t retag • s save • l load • y copy to… • c context • q quit"
}
