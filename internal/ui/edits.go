package ui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"slices"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/pkg/humanize"
)

// maxEditable is the largest file that can be opened in an editor; it
// matches what the inspection keeps in memory.
const maxEditable = 64 << 10

type (
	writeStageMsg struct {
		session int
		stage   string
	}
	writeDoneMsg struct {
		session int
		ref     image.Reference
		err     error
	}
	editDoneMsg struct {
		session  int
		step     int
		path     string
		tmp      string
		original []byte
		mode     uint32
		uid, gid int
		err      error
	}
	// imageWrittenMsg tells the app a new image was saved.
	imageWrittenMsg struct{ ref image.Reference }
)

type dialogKind int

const (
	dialogNone dialogKind = iota
	dialogConfirm
	dialogInput
	dialogWriting
)

// dialog is shown in place of the info pane and takes all keys.
type dialog struct {
	kind     dialogKind
	title    string
	lines    []string
	input    textinput.Model
	onYes    func(m *LayersModel) tea.Cmd
	onSubmit func(m *LayersModel, value string) tea.Cmd
	// onTab and footer add a toggle under the input.
	onTab  func(m *LayersModel)
	footer func(m *LayersModel) string
	stage  string
}

func (m *LayersModel) confirm(title string, lines []string, onYes func(m *LayersModel) tea.Cmd) {
	m.dialog = dialog{kind: dialogConfirm, title: title, lines: lines, onYes: onYes}
}

func (m *LayersModel) ask(title, value, placeholder string, onSubmit func(m *LayersModel, value string) tea.Cmd) tea.Cmd {
	in := newInput()
	in.Prompt = "> "
	in.Placeholder = placeholder
	in.SetValue(value)
	in.CursorEnd()
	_, rw := m.paneWidths()
	in.SetWidth(max(rw-4, 10))
	m.dialog = dialog{kind: dialogInput, title: title, input: in, onSubmit: onSubmit}
	return m.dialog.input.Focus()
}

func (m *LayersModel) updateDialog(msg tea.KeyPressMsg) tea.Cmd {
	d := &m.dialog
	switch d.kind {
	case dialogConfirm:
		switch msg.String() {
		case "y", "enter":
			onYes := d.onYes
			m.dialog = dialog{}
			return onYes(m)
		case "n", "esc", "q":
			m.dialog = dialog{}
		}
	case dialogInput:
		switch msg.String() {
		case "esc":
			m.dialog = dialog{}
			return nil
		case "tab":
			if d.onTab != nil {
				d.onTab(m)
			}
			return nil
		case "enter":
			onSubmit, value := d.onSubmit, d.input.Value()
			m.dialog = dialog{}
			return onSubmit(m, value)
		}
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		return cmd
	}
	return nil
}

func (m *LayersModel) dialogView(width int) string {
	d := m.dialog
	var b strings.Builder
	b.WriteString(boldStyle.Render(ansi.Truncate(d.title, width, "…")))
	for _, l := range d.lines {
		b.WriteString("\n" + ansi.Truncate(l, width, "…"))
	}
	switch d.kind {
	case dialogConfirm:
		b.WriteString("\n\n" + helpStyle.Render("[y] yes  [n] no"))
	case dialogInput:
		b.WriteString("\n" + d.input.View())
		if d.footer != nil {
			b.WriteString("\n" + d.footer(m))
		}
		b.WriteString("\n\n" + helpStyle.Render("enter apply • esc cancel"))
	case dialogWriting:
		b.WriteString("\n" + subtleStyle.Render(d.stage+"…"))
	}
	return b.String()
}

func (m *LayersModel) setStatus(msg string, ok bool) { m.status, m.statusOK = msg, ok }

// setDetails shows a new preview, keeping the cursor on the same file.
func (m *LayersModel) setDetails(details []image.LayerDetail) {
	keep := ""
	if n := m.currentNode(); n != nil {
		keep = n.Path
	}
	m.details = details
	m.layers = make([]image.Layer, len(details))
	for i, d := range details {
		m.layers[i] = d.Layer()
	}
	m.trees = map[int]*changeTree{}
	m.wasted = image.Wasted(details)
	m.marked = map[int]bool{}
	m.layer = max(0, min(m.layer, len(m.layers)-1))
	m.lines = nil
	m.refreshLines()
	if keep != "" {
		m.reveal(keep)
	}
}

// edit adds edits to the plan if the result still makes a valid image.
func (m *LayersModel) edit(done string, edits ...image.Edit) bool {
	if m.inspection == nil || len(edits) == 0 {
		return false
	}
	plan := m.plan.WithAll(edits...)
	details, err := m.inspection.Details(plan)
	if err != nil {
		m.setStatus(err.Error(), false)
		return false
	}
	m.plan = plan
	m.setDetails(details)
	m.setStatus(done+" · u undo", true)
	return true
}

func (m *LayersModel) undo(all bool) {
	if m.plan.Len() == 0 {
		m.setStatus("nothing to undo", false)
		return
	}
	plan := m.plan.Undo()
	if all {
		plan = image.EditPlan{}
	}
	details, err := m.inspection.Details(plan)
	if err != nil {
		m.setStatus(err.Error(), false)
		return
	}
	m.plan = plan
	m.setDetails(details)
	m.setStatus(fmt.Sprintf("%d pending edits", plan.Len()), true)
}

// reveal expands the directories leading to p and moves the cursor to it.
func (m *LayersModel) reveal(p string) {
	t := m.tree()
	if t == nil {
		return
	}
	n := t
	for _, part := range strings.Split(p, "/") {
		var next *changeTree
		for _, c := range n.Children {
			if c.Name == part {
				next = c
				break
			}
		}
		if next == nil {
			break
		}
		if next.Path != p {
			next.Expanded = true
		}
		n = next
	}
	m.relines(n)
}

// --- layer operations -------------------------------------------------------

func (m *LayersModel) toggleMark() {
	if m.layer >= len(m.layers) {
		return
	}
	if m.layers[m.layer].Empty() {
		m.setStatus("metadata-only step: nothing to mark", false)
		return
	}
	if m.marked[m.layer] {
		delete(m.marked, m.layer)
	} else {
		m.marked[m.layer] = true
	}
	m.selectLayer(m.layer + 1)
}

// targetSteps returns the marked steps, or the current one, highest first so
// that dropping them in order does not shift the others.
func (m *LayersModel) targetSteps() []int {
	var steps []int
	for s := range m.marked {
		steps = append(steps, s)
	}
	if len(steps) == 0 && m.layer < len(m.layers) {
		steps = []int{m.layer}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(steps)))
	return steps
}

func (m *LayersModel) dropLayers() {
	steps := m.targetSteps()
	var edits []image.Edit
	var warn []string
	for _, s := range steps {
		if m.layers[s].Empty() {
			m.setStatus(fmt.Sprintf("step %d is metadata-only: it has no layer to drop", s+1), false)
			return
		}
		edits = append(edits, image.DropLayer(s))
		warn = append(warn, m.dependents(s)...)
	}
	lines := []string{"Files these layers add or change disappear from the image."}
	if len(warn) > 0 {
		lines = append(lines, "", brokenStyle.Render("Later layers touch these files and may break:"))
		const shown = 5
		for _, w := range warn[:min(len(warn), shown)] {
			lines = append(lines, "  "+w)
		}
		if len(warn) > shown {
			lines = append(lines, fmt.Sprintf("  … and %d more", len(warn)-shown))
		}
	}
	title := fmt.Sprintf("Drop layer %d?", steps[0]+1)
	if len(steps) > 1 {
		title = fmt.Sprintf("Drop %d layers?", len(steps))
	}
	m.confirm(title, lines, func(m *LayersModel) tea.Cmd {
		m.edit(fmt.Sprintf("dropped %d layer(s)", len(edits)), edits...)
		return nil
	})
}

// dependents lists files of a step that later steps change or delete.
func (m *LayersModel) dependents(step int) []string {
	mine := map[string]bool{}
	for _, c := range m.details[step].Changes() {
		if c.Kind() != image.ChangeDeleted {
			mine[c.Path()] = true
		}
	}
	var out []string
	for j := step + 1; j < len(m.details); j++ {
		for _, c := range m.details[j].Changes() {
			if mine[c.Path()] && !c.Dir() {
				out = append(out, fmt.Sprintf("/%s (%s in step %d)", c.Path(), c.Kind(), j+1))
			}
		}
	}
	return out
}

func (m *LayersModel) mergeLayers() {
	steps := m.targetSteps()
	if len(steps) < 2 {
		m.setStatus("mark at least two layers with space, then press m", false)
		return
	}
	from, to := slices.Min(steps), slices.Max(steps)
	m.edit(fmt.Sprintf("merged layers %d–%d", from+1, to+1), image.MergeLayers(from, to))
}

// --- file operations ---------------------------------------------------------

func (m *LayersModel) removeFromLayer() {
	n := m.currentNode()
	if n == nil {
		return
	}
	e, err := image.RemoveFromLayer(m.layer, n.Path)
	if err != nil {
		m.setStatus(err.Error(), false)
		return
	}
	m.edit(fmt.Sprintf("removed /%s from layer %d", n.Path, m.layer+1), e)
}

func (m *LayersModel) purgePath() {
	n := m.currentNode()
	if n == nil {
		return
	}
	p := n.Path
	m.confirm("Purge /"+p+" from every layer?", []string{
		"No version of it will be left in the image,",
		"including the history of lower layers.",
	}, func(m *LayersModel) tea.Cmd {
		e, err := image.PurgePath(p)
		if err != nil {
			m.setStatus(err.Error(), false)
			return nil
		}
		m.edit("purged /"+p, e)
		return nil
	})
}

func (m *LayersModel) editFile() tea.Cmd {
	n := m.currentNode()
	if n == nil || n.Value == nil || n.Dir() || !n.Value.Link().IsZero() || n.Value.Kind() == image.ChangeDeleted {
		m.setStatus("select a file to edit", false)
		return nil
	}
	c := *n.Value
	var content []byte
	switch p := c.Preview(); {
	case p.State() == image.PreviewText:
		content = []byte(p.Text())
	case c.Size().Bytes() == 0:
	default:
		m.setStatus(fmt.Sprintf("only text files up to %s can be edited", humanize.Bytes(maxEditable)), false)
		return nil
	}
	tmp, err := os.CreateTemp("", "layve-*-"+path.Base(c.Path()))
	if err != nil {
		m.setStatus(err.Error(), false)
		return nil
	}
	_, err = tmp.Write(content)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		m.setStatus(err.Error(), false)
		return nil
	}
	uid, gid := c.Owner()
	done := editDoneMsg{
		session: m.session, step: m.layer, path: c.Path(), tmp: tmp.Name(),
		original: content, mode: uint32(c.Mode().Perm()), uid: uid, gid: gid,
	}
	return tea.ExecProcess(editorCommand(tmp.Name()), func(err error) tea.Msg {
		done.err = err
		return done
	})
}

func editorCommand(file string) *exec.Cmd {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	args := strings.Fields(editor)
	// Running the user's own $EDITOR is the point.
	return exec.Command(args[0], append(args[1:], file)...) //nolint:gosec,noctx // G702: user-chosen editor; runs until the user quits it
}

func (m *LayersModel) finishEdit(msg editDoneMsg) {
	defer os.Remove(msg.tmp)
	if msg.err != nil {
		m.setStatus("editor: "+msg.err.Error(), false)
		return
	}
	content, err := os.ReadFile(msg.tmp)
	if err != nil {
		m.setStatus(err.Error(), false)
		return
	}
	if bytes.Equal(content, msg.original) {
		m.setStatus("no changes", true)
		return
	}
	e, err := image.PutFile(msg.step, msg.path, content, os.FileMode(msg.mode), msg.uid, msg.gid)
	if err != nil {
		m.setStatus(err.Error(), false)
		return
	}
	m.edit(fmt.Sprintf("edited /%s in layer %d", msg.path, msg.step+1), e)
}

func (m *LayersModel) addFile() tea.Cmd {
	if m.layer >= len(m.layers) || m.layers[m.layer].Empty() {
		m.setStatus("select a step with a layer to add files to", false)
		return nil
	}
	dir := ""
	if n := m.currentNode(); n != nil {
		dir = n.Path
		if !n.Dir() {
			dir = path.Dir(n.Path)
		}
	}
	step := m.layer
	return m.ask(fmt.Sprintf("Add a file to layer %d: path on this machine", step+1), "", "/path/to/file",
		func(m *LayersModel, host string) tea.Cmd {
			host = strings.TrimSpace(host)
			info, err := os.Stat(host)
			switch {
			case err != nil:
				m.setStatus(err.Error(), false)
				return nil
			case !info.Mode().IsRegular():
				m.setStatus(host+" is not a regular file", false)
				return nil
			}
			dest := path.Join("/", dir, info.Name())
			return m.ask("Path in the image", dest, "/etc/app.conf", func(m *LayersModel, dest string) tea.Cmd {
				m.putHostFile(step, host, dest, info)
				return nil
			})
		})
}

func (m *LayersModel) putHostFile(step int, host, dest string, info os.FileInfo) {
	var e image.Edit
	var err error
	if info.Size() <= maxEditable {
		content, rerr := os.ReadFile(host)
		if rerr != nil {
			m.setStatus(rerr.Error(), false)
			return
		}
		e, err = image.PutFile(step, dest, content, info.Mode(), 0, 0)
	} else {
		e, err = image.PutHostFile(step, dest, host, info.Mode(), info.Size())
	}
	if err != nil {
		m.setStatus(err.Error(), false)
		return
	}
	m.edit(fmt.Sprintf("added %s to layer %d", dest, step+1), e)
}

// --- wasted space --------------------------------------------------------------

func (m *LayersModel) fixWasted(items []image.WastedFile) {
	edits := make([]image.Edit, 0, len(items))
	for _, w := range items {
		e, err := image.RemoveFromLayer(w.Step, w.Path)
		if err == nil {
			edits = append(edits, e)
		}
	}
	saved := image.WastedTotal(items)
	if m.edit(fmt.Sprintf("removed %d hidden file(s), %s saved", len(edits), saved), edits...) {
		return
	}
	// A batch can fail on one file (say, a hard link target); keep the rest.
	ok := 0
	for _, e := range edits {
		plan := m.plan.With(e)
		if _, err := m.inspection.Details(plan); err == nil {
			m.plan = plan
			ok++
		}
	}
	details, err := m.inspection.Details(m.plan)
	if err != nil {
		m.setStatus(err.Error(), false)
		return
	}
	m.setDetails(details)
	m.setStatus(fmt.Sprintf("removed %d of %d hidden files; the rest are hard link targets · u undo", ok, len(edits)), ok > 0)
}

func (m *LayersModel) handleWastedKey(k string) {
	page := m.filesHeight()
	switch k {
	case "up", "k":
		m.wastedCursor--
	case "down", "j":
		m.wastedCursor++
	case "pgup", "ctrl+u":
		m.wastedCursor -= page
	case "pgdown", "ctrl+d":
		m.wastedCursor += page
	case "home", "g":
		m.wastedCursor = 0
	case "end", "G":
		m.wastedCursor = len(m.wasted) - 1
	case "d":
		if m.wastedCursor < len(m.wasted) {
			m.fixWasted([]image.WastedFile{m.wasted[m.wastedCursor]})
		}
	case "f":
		m.fixWasted(m.wasted)
	case "enter":
		if m.wastedCursor < len(m.wasted) {
			w := m.wasted[m.wastedCursor]
			m.showWasted = false
			m.selectLayer(w.Step)
			m.reveal(w.Path)
			m.focus = paneFiles
		}
	case "W", "esc":
		m.showWasted = false
	}
	m.wastedCursor = max(0, min(m.wastedCursor, len(m.wasted)-1))
	h := m.filesHeight()
	if m.wastedCursor < m.wastedOffset {
		m.wastedOffset = m.wastedCursor
	}
	if m.wastedCursor >= m.wastedOffset+h {
		m.wastedOffset = m.wastedCursor - h + 1
	}
}

func (m *LayersModel) wastedView(width int) string {
	if len(m.wasted) == 0 {
		return okStyle.Render("No wasted space: every file version is visible.")
	}
	var b strings.Builder
	b.WriteString(brokenStyle.Render(fmt.Sprintf("Wasted %s in %d hidden file versions", image.WastedTotal(m.wasted), len(m.wasted))))
	b.WriteString("\n" + subtleStyle.Render(fmt.Sprintf("%-6s %7s  %-10s %s", "Layer", "Size", "Hidden by", "Path")))
	h := m.filesHeight()
	for i := m.wastedOffset; i < min(m.wastedOffset+h, len(m.wasted)); i++ {
		w := m.wasted[i]
		line := fmt.Sprintf("%-6d %7s  %-10s /%s", w.Step+1, w.Size, fmt.Sprintf("step %d", w.By+1), w.Path)
		line = ansi.Truncate(line, width, "…")
		if i == m.wastedCursor && m.focus == paneFiles {
			line = cursorStyle.Render(padRight(line, width))
		}
		b.WriteString("\n" + line)
	}
	return b.String()
}

// --- writing -------------------------------------------------------------------

func (m *LayersModel) defaultTarget() string {
	if refs := m.img.References(); len(refs) > 0 {
		return refs[0].Repository() + ":" + refs[0].Tag() + "-edited"
	}
	return "layve-edited:" + m.img.ID().Short()
}

func (m *LayersModel) startWrite() tea.Cmd {
	if m.inspection == nil {
		return nil
	}
	if m.plan.Len() == 0 {
		m.setStatus("no pending edits to write", false)
		return nil
	}
	cmd := m.ask(fmt.Sprintf("Write %d edit(s) as a new image", m.plan.Len()), m.defaultTarget(), "repository:tag",
		func(m *LayersModel, value string) tea.Cmd {
			ref, err := image.ParseReference(value)
			if err != nil {
				m.setStatus(err.Error(), false)
				return nil
			}
			return m.write(ref)
		})
	m.dialog.onTab = func(m *LayersModel) { m.markHistory = !m.markHistory }
	m.dialog.footer = func(m *LayersModel) string {
		state := "off"
		if m.markHistory {
			state = "on"
		}
		return subtleStyle.Render(fmt.Sprintf("history note %q: %s (tab to toggle)", image.HistoryMark, state))
	}
	return cmd
}

func (m *LayersModel) write(ref image.Reference) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.writeCancel = cancel
	m.dialog = dialog{kind: dialogWriting, title: "Writing " + ref.String(), stage: "starting"}
	svc, id, plan, session, events := m.svc, m.img.ID(), m.plan, m.session, m.events
	opts := image.WriteOptions{MarkHistory: m.markHistory}
	go func() {
		err := svc.Write(ctx, id, plan, ref, opts, func(stage string) {
			select {
			case events <- writeStageMsg{session: session, stage: stage}:
			default:
			}
		})
		select {
		case events <- writeDoneMsg{session: session, ref: ref, err: err}:
		case <-ctx.Done():
		}
	}()
	return m.waitEvent()
}

func (m *LayersModel) handleWriteMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case writeStageMsg:
		if msg.session != m.session {
			return nil
		}
		m.dialog.stage = msg.stage
		return m.waitEvent()
	case writeDoneMsg:
		if msg.session != m.session {
			return nil
		}
		m.dialog = dialog{}
		if msg.err != nil {
			m.setStatus("write failed: "+msg.err.Error(), false)
			return nil
		}
		ref := msg.ref
		return func() tea.Msg { return imageWrittenMsg{ref: ref} }
	}
	return nil
}
