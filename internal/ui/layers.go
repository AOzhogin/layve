package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/pkg/filetree"
	"github.com/AOzhogin/layve/internal/pkg/humanize"
)

// expandAllLimit is the node count up to which a layer's tree opens fully;
// bigger trees (base images) open one level deep.
const expandAllLimit = 300

type (
	historyMsg struct {
		session int
		layers  []image.Layer
		err     error
	}
	detailsProgressMsg struct {
		session int
		read    int64
	}
	detailsMsg struct {
		session    int
		inspection Inspection
		err        error
	}
	layersTickMsg struct{ session int }
	// closeLayersMsg asks the app to return to the image list.
	closeLayersMsg struct{}
)

type changeTree = filetree.Node[image.FileChange]

type pane int

const (
	paneLayers pane = iota
	paneFiles
	paneInfo
	paneCount
)

// LayersModel shows an image's history and the files each layer changed.
type LayersModel struct {
	svc     ImageService
	img     image.Image
	session int
	cancel  context.CancelFunc
	events  chan tea.Msg

	layers     []image.Layer
	details    []image.LayerDetail
	inspection Inspection
	plan       image.EditPlan
	wasted     []image.WastedFile
	marked     map[int]bool
	dialog     dialog
	status     string
	statusOK   bool

	showWasted   bool
	wastedCursor int
	wastedOffset int
	writeCancel  context.CancelFunc
	markHistory  bool
	histErr      error
	fileErr      error
	read         int64
	started      time.Time
	finished     time.Duration

	focus      pane
	layer      int
	trees      map[int]*changeTree
	lines      []filetree.Line[image.FileChange]
	fileCursor int
	fileOffset int
	infoOffset int

	width, height int
}

// NewLayers creates the layers screen. session tags async messages so that
// results for a closed screen are ignored.
func NewLayers(svc ImageService, img image.Image, session, width, height int) *LayersModel {
	return &LayersModel{
		svc:     svc,
		img:     img,
		session: session,
		events:  make(chan tea.Msg, 1),
		trees:   map[int]*changeTree{},
		marked:  map[int]bool{},
		width:   width,
		height:  height,
	}
}

// Init loads the history right away and starts reading files in the background.
func (m *LayersModel) Init() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.started = time.Now()
	svc, id, session, events := m.svc, m.img.ID(), m.session, m.events

	history := func() tea.Msg {
		hctx, hcancel := context.WithTimeout(ctx, listTimeout)
		defer hcancel()
		layers, err := svc.History(hctx, id)
		return historyMsg{session: session, layers: layers, err: err}
	}
	go func() {
		inspection, err := svc.Inspect(ctx, id, func(read int64) {
			select {
			case events <- detailsProgressMsg{session: session, read: read}:
			default: // drop progress updates the UI has not consumed yet
			}
		})
		select {
		case events <- detailsMsg{session: session, inspection: inspection, err: err}:
		case <-ctx.Done():
		}
	}()
	return tea.Batch(history, m.waitEvent(), m.tick())
}

// tick refreshes the elapsed time while Docker prepares the export, which
// can take seconds before the first byte arrives.
func (m *LayersModel) tick() tea.Cmd {
	session := m.session
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return layersTickMsg{session: session} })
}

func (m *LayersModel) waitEvent() tea.Cmd {
	events := m.events
	return func() tea.Msg { return <-events }
}

// Close stops background work.
func (m *LayersModel) Close() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.writeCancel != nil {
		m.writeCancel()
	}
}

// Update handles messages addressed to this screen.
func (m *LayersModel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampFiles()
	case historyMsg:
		if msg.session == m.session && m.details == nil {
			m.layers, m.histErr = msg.layers, msg.err
			m.layer = max(len(m.layers)-1, 0)
		}
	case layersTickMsg:
		if msg.session == m.session && m.details == nil && m.fileErr == nil {
			return m.tick()
		}
	case detailsProgressMsg:
		if msg.session == m.session {
			m.read = msg.read
			return m.waitEvent()
		}
	case detailsMsg:
		if msg.session != m.session {
			return nil
		}
		m.finished = time.Since(m.started)
		m.fileErr = msg.err
		if msg.err == nil {
			m.inspection = msg.inspection
			details, err := m.inspection.Details(m.plan)
			if err != nil {
				m.fileErr = err
				return nil
			}
			m.setDetails(details)
			m.selectLayer(m.layer)
		}
	case writeStageMsg, writeDoneMsg:
		return m.handleWriteMsg(msg)
	case editDoneMsg:
		if msg.session == m.session {
			m.finishEdit(msg)
		}
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return nil
}

func (m *LayersModel) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	m.status = ""
	if m.dialog.kind != dialogNone {
		return m.updateDialog(msg)
	}
	ready := m.inspection != nil && m.details != nil
	switch k {
	case "esc", "q":
		if m.showWasted {
			m.showWasted = false
			return nil
		}
		if m.plan.Len() > 0 {
			m.confirm(fmt.Sprintf("Discard %d pending edit(s)?", m.plan.Len()), nil, func(*LayersModel) tea.Cmd {
				return func() tea.Msg { return closeLayersMsg{} }
			})
			return nil
		}
		return func() tea.Msg { return closeLayersMsg{} }
	case "u", "U":
		if ready {
			m.undo(k == "U")
		}
		return nil
	case "w":
		if ready {
			return m.startWrite()
		}
		return nil
	case "W":
		if ready {
			m.showWasted = !m.showWasted
			m.focus = paneFiles
			m.wastedCursor, m.wastedOffset = 0, 0
		}
		return nil
	case "tab":
		m.focus = (m.focus + 1) % paneCount
		return nil
	case "shift+tab":
		m.focus = (m.focus + paneCount - 1) % paneCount
		return nil
	}
	if m.showWasted && m.focus == paneFiles {
		m.handleWastedKey(k)
		return nil
	}
	if m.focus == paneInfo {
		m.scrollInfo(k)
		return nil
	}
	if m.focus == paneLayers {
		switch k {
		case "up", "k":
			m.selectLayer(m.layer - 1)
		case "down", "j":
			m.selectLayer(m.layer + 1)
		case "home", "g":
			m.selectLayer(0)
		case "end", "G":
			m.selectLayer(len(m.layers) - 1)
		case "enter", "right", "l":
			m.focus = paneFiles
		case "space":
			if ready {
				m.toggleMark()
			}
		case "x":
			if ready {
				m.dropLayers()
			}
		case "m":
			if ready {
				m.mergeLayers()
			}
		}
		return nil
	}
	if ready {
		switch k {
		case "d":
			m.removeFromLayer()
			return nil
		case "D":
			m.purgePath()
			return nil
		case "e":
			return m.editFile()
		case "a":
			return m.addFile()
		}
	}

	before := m.currentNode()
	defer func() {
		if m.currentNode() != before {
			m.infoOffset = 0
		}
	}()
	page := m.filesHeight()
	switch k {
	case "up", "k":
		m.fileCursor--
	case "down", "j":
		m.fileCursor++
	case "pgup", "ctrl+u":
		m.fileCursor -= page
	case "pgdown", "ctrl+d":
		m.fileCursor += page
	case "home", "g":
		m.fileCursor = 0
	case "end", "G":
		m.fileCursor = len(m.lines) - 1
	case "enter", "space":
		m.toggleCurrent()
	case "right", "l":
		m.setCurrentExpanded(true)
	case "left", "h":
		m.collapseOrParent()
	case "E":
		m.setAllExpanded(true)
	case "C":
		m.setAllExpanded(false)
	}
	m.clampFiles()
	return nil
}

func (m *LayersModel) selectLayer(i int) {
	if len(m.layers) == 0 {
		return
	}
	m.layer = max(0, min(i, len(m.layers)-1))
	m.fileCursor, m.fileOffset, m.infoOffset = 0, 0, 0
	m.refreshLines()
}

func (m *LayersModel) tree() *changeTree {
	if m.details == nil || m.layer >= len(m.details) {
		return nil
	}
	if t, ok := m.trees[m.layer]; ok {
		return t
	}
	t := filetree.Build(m.details[m.layer].Changes(), func(c image.FileChange) (string, bool) {
		return c.Path(), c.Dir()
	})
	if t.Count() <= expandAllLimit {
		t.SetExpanded(true)
	} else {
		t.ExpandToDepth(1)
	}
	m.trees[m.layer] = t
	return t
}

func (m *LayersModel) refreshLines() {
	m.lines = nil
	if t := m.tree(); t != nil {
		m.lines = t.Visible()
	}
	m.clampFiles()
}

func (m *LayersModel) currentNode() *changeTree {
	if m.fileCursor < 0 || m.fileCursor >= len(m.lines) {
		return nil
	}
	return m.lines[m.fileCursor].Node
}

// relines rebuilds the visible lines and keeps the cursor on the same node.
func (m *LayersModel) relines(keep *changeTree) {
	m.refreshLines()
	for i, l := range m.lines {
		if l.Node == keep {
			m.fileCursor = i
			break
		}
	}
	m.clampFiles()
}

func (m *LayersModel) toggleCurrent() {
	if n := m.currentNode(); n != nil && n.Dir() {
		n.Expanded = !n.Expanded
		m.relines(n)
	}
}

func (m *LayersModel) setCurrentExpanded(v bool) {
	if n := m.currentNode(); n != nil && n.Dir() && n.Expanded != v {
		n.Expanded = v
		m.relines(n)
	}
}

func (m *LayersModel) collapseOrParent() {
	n := m.currentNode()
	if n == nil {
		return
	}
	if n.Dir() && n.Expanded {
		m.setCurrentExpanded(false)
		return
	}
	depth := m.lines[m.fileCursor].Depth
	for i := m.fileCursor - 1; i >= 0; i-- {
		if m.lines[i].Depth < depth {
			m.fileCursor = i
			return
		}
	}
}

func (m *LayersModel) setAllExpanded(v bool) {
	if t := m.tree(); t != nil {
		keep := m.currentNode()
		t.SetExpanded(v)
		m.relines(keep)
	}
}

func (m *LayersModel) clampFiles() {
	h := m.filesHeight()
	m.fileCursor = max(0, min(m.fileCursor, len(m.lines)-1))
	if m.fileCursor < m.fileOffset {
		m.fileOffset = m.fileCursor
	}
	if m.fileCursor >= m.fileOffset+h {
		m.fileOffset = m.fileCursor - h + 1
	}
	m.fileOffset = max(0, min(m.fileOffset, len(m.lines)-h))
}

// Layout: title and help take one line each. The layers pane fills the
// left column; on the right the file tree sits above the info pane.

// paneHeight is the inner height of the layers pane.
func (m *LayersModel) paneHeight() int { return max(m.height-4, 8) }

// infoHeight is the inner height of the info pane: about two fifths of the
// right column.
func (m *LayersModel) infoHeight() int {
	return max((m.paneHeight()-2)*2/5, 4)
}

// treeHeight is the inner height of the file tree pane.
func (m *LayersModel) treeHeight() int {
	return max(m.paneHeight()-2-m.infoHeight(), 3)
}

// filesHeight leaves two lines for the summary and column header.
func (m *LayersModel) filesHeight() int { return max(m.treeHeight()-2, 1) }

// paneWidths returns the inner widths of the left and right columns.
func (m *LayersModel) paneWidths() (left, right int) {
	inner := max(m.width-4, 80)
	left = max(inner*30/100, 24)
	return left, max(inner-left, 50)
}

// View renders the screen.
func (m *LayersModel) View() string {
	name := m.img.ID().Short()
	if refs := m.img.References(); len(refs) > 0 {
		name = refs[0].String()
	}
	title := titleStyle.Render("Layers of "+name) + subtleStyle.Render(m.statusText())
	if len(m.wasted) > 0 {
		title += brokenStyle.Render(fmt.Sprintf("  wasted %s in %d files (W)", image.WastedTotal(m.wasted), len(m.wasted)))
	}
	if n := m.plan.Len(); n > 0 {
		title += markStyle.Render(fmt.Sprintf("  ✎ %d pending edits · u undo · w write", n))
	}

	lw, rw := m.paneWidths()
	box := func(p pane, w, h int, content string) string {
		focused := m.focus == p || (p == paneInfo && m.dialog.kind != dialogNone)
		return paneStyle(focused).Width(w + 2).Height(h + 2).Render(content)
	}
	right := lipgloss.JoinVertical(lipgloss.Left,
		box(paneFiles, rw, m.treeHeight(), m.filesView(rw)),
		box(paneInfo, rw, m.infoHeight(), m.infoView(rw, m.infoHeight())),
	)
	panes := lipgloss.JoinHorizontal(lipgloss.Top,
		box(paneLayers, lw, m.paneHeight(), m.layersView(lw, m.paneHeight())),
		right,
	)
	help := helpStyle.Render(m.helpText())
	if m.status != "" {
		style := errStyle
		if m.statusOK {
			style = okStyle
		}
		help = style.Render(ansi.Truncate(m.status, m.width-2, "…"))
	}
	title = ansi.Truncate(title, m.width, "…")
	return lipgloss.JoinVertical(lipgloss.Left, title, panes, help)
}

func (m *LayersModel) statusText() string {
	switch {
	case m.fileErr != nil:
		return ""
	case m.details == nil:
		return "  reading files… " + time.Since(m.started).Round(100*time.Millisecond).String()
	default:
		return fmt.Sprintf("  files read in %s", m.finished.Round(100*time.Millisecond))
	}
}

func (m *LayersModel) layersView(width, height int) string {
	if m.histErr != nil && m.details == nil {
		return errStyle.Render(ansi.Wordwrap(m.histErr.Error(), width-2, ""))
	}
	if len(m.layers) == 0 {
		return helpStyle.Render("Loading…")
	}
	start := max(0, min(m.layer-height/2, len(m.layers)-height))
	end := min(start+height, len(m.layers))
	var b strings.Builder
	for i := start; i < end; i++ {
		l := m.layers[i]
		mark := " "
		switch {
		case m.marked[i]:
			mark = "●"
		case i < len(m.details) && m.details[i].Edited():
			mark = "✎"
		}
		cmd := l.CreatedBy()
		if i < len(m.details) && m.details[i].Merged() > 1 {
			cmd = fmt.Sprintf("[%d merged] %s", m.details[i].Merged(), cmd)
		}
		line := fmt.Sprintf("%s%3d %7s  %s", mark, i+1, l.Size(), cmd)
		line = ansi.Truncate(line, width, "…")
		switch {
		case i == m.layer:
			line = cursorStyle.Render(padRight(line, width))
		case l.Empty():
			line = subtleStyle.Render(line)
		}
		b.WriteString(line)
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (m *LayersModel) filesView(width int) string {
	if m.showWasted {
		return m.wastedView(width)
	}
	switch {
	case m.fileErr != nil:
		return errStyle.Render(ansi.Wordwrap("Cannot read files: "+m.fileErr.Error(), width-2, ""))
	case m.details == nil:
		return helpStyle.Render(m.progressBar(width - 2))
	case m.layer < len(m.layers) && m.layers[m.layer].Empty():
		return helpStyle.Render("Metadata-only step: no file changes.")
	case len(m.lines) == 0:
		return helpStyle.Render("No file changes.")
	}

	h := m.filesHeight()
	visible := m.lines[m.fileOffset:min(m.fileOffset+h, len(m.lines))]
	cols := m.attrColumns(visible, width)

	var b strings.Builder
	b.WriteString(m.summary())
	b.WriteByte('\n')
	b.WriteString(subtleStyle.Render(cols.header()))
	for i, l := range visible {
		b.WriteByte('\n')
		b.WriteString(m.fileLine(l, width, cols, m.fileOffset+i == m.fileCursor && m.focus == paneFiles))
	}
	return b.String()
}

func (m *LayersModel) summary() string {
	counts := map[image.ChangeKind]int{}
	for _, c := range m.details[m.layer].Changes() {
		counts[c.Kind()]++
	}
	return addedStyle.Render(fmt.Sprintf("+%d added ", counts[image.ChangeAdded])) +
		modStyle.Render(fmt.Sprintf("~%d modified ", counts[image.ChangeModified])) +
		lipgloss.NewStyle().Foreground(colorDeleted).Render(fmt.Sprintf("-%d deleted", counts[image.ChangeDeleted]))
}

// attrColumns sizes the attribute columns shown left of the tree. The
// owner column fits the widest uid:gid on screen; narrow panes drop the
// owner first, then permissions.
type attrColumns struct {
	mode, owner bool
	ownerWidth  int
}

const (
	modeWidth = 10 // "-rwxr-xr-x"
	sizeWidth = 7
)

func (m *LayersModel) attrColumns(lines []filetree.Line[image.FileChange], width int) attrColumns {
	c := attrColumns{mode: width >= 60, owner: width >= 80, ownerWidth: len("UID:GID")}
	for _, l := range lines {
		c.ownerWidth = max(c.ownerWidth, len(ownerText(l.Node)))
	}
	return c
}

func (c attrColumns) header() string {
	return c.row("", "Mode", "UID:GID", "Size") + "Path"
}

// row lays out the marker and attribute columns.
func (c attrColumns) row(marker, mode, owner, size string) string {
	out := fmt.Sprintf("%-1s ", marker)
	if c.mode {
		out += fmt.Sprintf("%-*s ", modeWidth, mode)
	}
	if c.owner {
		out += fmt.Sprintf("%-*s ", c.ownerWidth, owner)
	}
	return out + fmt.Sprintf("%*s  ", sizeWidth, size)
}

func ownerText(n *changeTree) string {
	if n.Value == nil || n.Value.Kind() == image.ChangeDeleted {
		return ""
	}
	uid, gid := n.Value.Owner()
	return fmt.Sprintf("%d:%d", uid, gid)
}

func (m *LayersModel) fileLine(l filetree.Line[image.FileChange], width int, cols attrColumns, cursor bool) string {
	n := l.Node
	marker, style := " ", lipgloss.NewStyle()
	mode, size := "", ""
	if v := n.Value; v != nil {
		marker = v.Kind().Symbol()
		style = kindStyle(v.Kind())
		if v.Kind() != image.ChangeDeleted {
			mode = humanize.FileMode(v.Mode())
			if !n.Dir() && v.Link().IsZero() {
				size = v.Size().String()
			}
		}
	}
	attrs := cols.row(marker, mode, ownerText(n), size)

	name := n.Name
	if n.Dir() {
		icon := ""
		switch {
		case len(n.Children) == 0:
		case n.Expanded:
			icon = "▾ "
		default:
			icon = "▸ "
		}
		name = icon + name + "/"
		if n.Value == nil {
			style = dirStyle
		}
	}

	arrow, target, targetStyle := linkSuffix(n)
	avail := max(width-ansi.StringWidth(attrs)-ansi.StringWidth(l.Prefix), 1)
	name = ansi.Truncate(name, avail, "…")
	target = ansi.Truncate(target, max(avail-ansi.StringWidth(name)-ansi.StringWidth(arrow), 0), "…")
	if target == "" {
		arrow = ""
	}
	if cursor {
		return cursorStyle.Render(padRight(attrs+l.Prefix+name+arrow+target, width))
	}
	return style.Render(marker) + subtleStyle.Render(strings.TrimPrefix(attrs, marker)) +
		subtleStyle.Render(l.Prefix) + style.Render(name) +
		subtleStyle.Render(arrow) + targetStyle.Render(target)
}

func kindStyle(k image.ChangeKind) lipgloss.Style {
	switch k {
	case image.ChangeAdded:
		return addedStyle
	case image.ChangeModified:
		return modStyle
	case image.ChangeDeleted:
		return deletedStyle
	default:
		return lipgloss.NewStyle()
	}
}

func (m *LayersModel) progressBar(width int) string {
	elapsed := time.Since(m.started).Round(100 * time.Millisecond)
	total := m.img.Size().Bytes()
	if m.read == 0 || total <= 0 || width < 10 {
		// The daemon exports the whole image before sending the first byte.
		return fmt.Sprintf("Docker is exporting the image… %s\n\nFile changes appear here when it is done;\nthe layer list on the left is ready.", elapsed)
	}
	frac := min(float64(m.read)/float64(total), 1)
	barW := width - 8
	filled := int(frac * float64(barW))
	bar := addedStyle.Render(strings.Repeat("█", filled)) + subtleStyle.Render(strings.Repeat("░", barW-filled))
	return fmt.Sprintf("Reading image files… %s\n\n%s %3.0f%%", elapsed, bar, frac*100)
}

func padRight(s string, width int) string {
	return s + strings.Repeat(" ", max(width-ansi.StringWidth(s), 0))
}

func (m *LayersModel) helpText() string {
	switch {
	case m.showWasted && m.focus == paneFiles:
		return "↑/↓ move • enter show in tree • d remove this one • f remove all • W/esc back"
	case m.focus == paneLayers:
		return "space mark • x drop • m merge marked • u undo • w write • W wasted • tab next pane • esc back"
	case m.focus == paneFiles:
		return "enter fold • d remove from layer • D purge from image • e edit • a add file • u undo • w write • W wasted • esc back"
	default:
		return "↑/↓ scroll • tab next pane • esc back"
	}
}
