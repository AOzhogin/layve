package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/domain/registry"
	"github.com/AOzhogin/layve/internal/pkg/humanize"
)

// Hub searches a registry for images to pull.
type Hub interface {
	Search(ctx context.Context, query string) ([]registry.Repository, error)
	Tags(ctx context.Context, repo, filter string) ([]registry.Tag, error)
}

const hubTimeout = 30 * time.Second

type hubStage int

const (
	stageRepos hubStage = iota
	stageTags
	stagePlatforms
)

type (
	hubReposMsg struct {
		seq   int
		repos []registry.Repository
		err   error
	}
	hubTagsMsg struct {
		seq  int
		tags []registry.Tag
		err  error
	}
	hubPlatformMsg struct {
		platform registry.Platform
		err      error
	}
	pullProgressMsg struct{ done, total int64 }
	pullDoneMsg     struct {
		ref      image.Reference
		platform registry.Platform
		err      error
	}
	// closeHubMsg returns to the image list; pulled tells it to refresh.
	closeHubMsg struct{ pulled bool }
)

// HubModel is the Docker Hub screen: search, pick a tag, pick a platform,
// pull into the current context.
type HubModel struct {
	hub     Hub
	svc     ImageService
	context string
	events  chan tea.Msg

	stage     hubStage
	input     textinput.Model
	typing    bool // keys go to the input
	seq       int
	loading   bool
	repos     []registry.Repository
	repo      registry.Repository
	tags      []registry.Tag
	tag       registry.Tag
	cursor    [3]int
	native    registry.Platform
	hasNat    bool
	pulling   bool
	progress  [2]int64
	started   time.Time
	pulled    bool
	lastQuery string
	status    string
	ok        bool

	width, height int
}

// NewHub creates the Docker Hub screen.
func NewHub(hub Hub, svc ImageService, contextName string, width, height int) *HubModel {
	in := newInput()
	in.Prompt = "search: "
	in.Placeholder = "nginx, postgres, bitnami/redis…"
	in.SetWidth(max(width-14, 20))
	return &HubModel{hub: hub, svc: svc, context: contextName, events: make(chan tea.Msg, 1),
		input: in, typing: true, width: width, height: height}
}

// Init focuses the search box and asks the daemon for its platform.
func (m *HubModel) Init() tea.Cmd {
	svc := m.svc
	return tea.Batch(m.input.Focus(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
		defer cancel()
		p, err := svc.Platform(ctx)
		return hubPlatformMsg{platform: p, err: err}
	})
}

func (m *HubModel) search(query string) tea.Cmd {
	m.seq++
	m.loading, m.status = true, ""
	seq, hub := m.seq, m.hub
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), hubTimeout)
		defer cancel()
		repos, err := hub.Search(ctx, query)
		return hubReposMsg{seq: seq, repos: repos, err: err}
	}
}

func (m *HubModel) loadTags(filter string) tea.Cmd {
	m.seq++
	m.loading, m.status = true, ""
	seq, hub, repo := m.seq, m.hub, m.repo.Name()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), hubTimeout)
		defer cancel()
		tags, err := hub.Tags(ctx, repo, filter)
		return hubTagsMsg{seq: seq, tags: tags, err: err}
	}
}

// Update handles messages for the screen.
func (m *HubModel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(msg.Width-14, 20))
	case hubPlatformMsg:
		m.native, m.hasNat = msg.platform, msg.err == nil
	case hubReposMsg:
		if msg.seq == m.seq {
			m.loading = false
			m.repos, m.cursor[stageRepos] = msg.repos, 0
			m.setErr(msg.err)
			if msg.err == nil && len(msg.repos) == 0 {
				m.status, m.ok = "nothing found", false
			}
			if len(msg.repos) > 0 {
				m.typing = false
				m.input.Blur()
			}
		}
	case hubTagsMsg:
		if msg.seq == m.seq {
			m.loading = false
			m.tags, m.cursor[stageTags] = msg.tags, 0
			m.setErr(msg.err)
			if msg.err == nil && len(msg.tags) == 0 {
				m.status, m.ok = "no tags match", false
			}
			if len(msg.tags) > 0 {
				m.typing = false
				m.input.Blur()
			}
		}
	case pullProgressMsg:
		m.progress = [2]int64{msg.done, msg.total}
		return m.waitEvent()
	case pullDoneMsg:
		m.pulling = false
		if msg.err != nil {
			m.status, m.ok = msg.err.Error(), false
			return nil
		}
		m.pulled = true
		m.status, m.ok = fmt.Sprintf("Pulled %s for %s into %s in %s", msg.ref, msg.platform, m.where(),
			time.Since(m.started).Round(100*time.Millisecond)), true
		if m.hasNat && !msg.platform.Matches(m.native) {
			m.status += fmt.Sprintf(" · it will not run on this %s machine; save (s) or copy (y) it to a %s one", m.native, msg.platform)
		}
		m.stage = stageTags
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return nil
}

func (m *HubModel) setErr(err error) {
	if err != nil {
		m.status, m.ok = err.Error(), false
	}
}

func (m *HubModel) where() string {
	if m.context == "" {
		return "local Docker"
	}
	return m.context
}

func (m *HubModel) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if m.pulling {
		return nil
	}
	if m.typing {
		switch k {
		case "enter":
			q := strings.TrimSpace(m.input.Value())
			if m.stage == stageRepos {
				if q == "" {
					return nil
				}
				return m.search(q)
			}
			return m.loadTags(q)
		case "esc":
			if m.stage == stageRepos && len(m.repos) == 0 {
				return m.close()
			}
			m.typing = false
			m.input.Blur()
			return nil
		case "down", "tab":
			m.typing = false
			m.input.Blur()
			return nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return cmd
	}

	n := m.listLen()
	c := &m.cursor[m.stage]
	switch k {
	case "up", "k":
		if *c == 0 && m.stage != stagePlatforms {
			return m.focusInput()
		}
		*c = max(*c-1, 0)
	case "down", "j":
		*c = min(*c+1, n-1)
	case "pgup":
		*c = max(*c-m.listHeight(), 0)
	case "pgdown":
		*c = min(*c+m.listHeight(), n-1)
	case "/", "tab":
		if m.stage != stagePlatforms {
			return m.focusInput()
		}
	case "esc", "backspace", "left":
		return m.back()
	case "q":
		return m.close()
	case "enter", "right":
		return m.open()
	}
	return nil
}

func (m *HubModel) focusInput() tea.Cmd {
	m.typing = true
	return m.input.Focus()
}

func (m *HubModel) back() tea.Cmd {
	switch m.stage {
	case stagePlatforms:
		m.stage = stageTags
	case stageTags:
		m.stage = stageRepos
		m.input.Prompt, m.input.Placeholder = "search: ", "nginx, postgres, bitnami/redis…"
		m.input.SetValue(m.query())
	default:
		return m.close()
	}
	return nil
}

// query remembers the last search while browsing tags.
func (m *HubModel) query() string {
	if m.stage == stageRepos {
		return m.input.Value()
	}
	return m.lastQuery
}

func (m *HubModel) close() tea.Cmd {
	pulled := m.pulled
	return func() tea.Msg { return closeHubMsg{pulled: pulled} }
}

func (m *HubModel) open() tea.Cmd {
	c := m.cursor[m.stage]
	switch m.stage {
	case stageRepos:
		if c >= len(m.repos) {
			return nil
		}
		m.lastQuery = m.input.Value()
		m.repo, m.stage, m.tags = m.repos[c], stageTags, nil
		m.input.Prompt, m.input.Placeholder = "filter tags: ", "1.27, alpine, …"
		m.input.SetValue("")
		return m.loadTags("")
	case stageTags:
		if c >= len(m.tags) {
			return nil
		}
		m.tag, m.stage = m.tags[c], stagePlatforms
		m.cursor[stagePlatforms] = 0
		for i, v := range m.tag.Variants() {
			if m.hasNat && v.Platform.Matches(m.native) {
				m.cursor[stagePlatforms] = i
				break
			}
		}
		if len(m.tag.Variants()) == 0 {
			m.status, m.ok = "Docker Hub lists no platforms for this tag", false
			m.stage = stageTags
		}
	case stagePlatforms:
		variants := m.tag.Variants()
		if c >= len(variants) {
			return nil
		}
		return m.pull(variants[c].Platform)
	}
	return nil
}

func (m *HubModel) pull(p registry.Platform) tea.Cmd {
	ref, err := image.ParseReference(m.repo.Name() + ":" + m.tag.Name())
	if err != nil {
		m.status, m.ok = err.Error(), false
		return nil
	}
	m.pulling, m.started, m.progress, m.status = true, time.Now(), [2]int64{}, ""
	svc, events := m.svc, m.events
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), copyTimeout)
		defer cancel()
		err := svc.Pull(ctx, ref, p, func(done, total int64) {
			select {
			case events <- pullProgressMsg{done: done, total: total}:
			default:
			}
		})
		events <- pullDoneMsg{ref: ref, platform: p, err: err}
	}()
	return m.waitEvent()
}

func (m *HubModel) waitEvent() tea.Cmd {
	events := m.events
	return func() tea.Msg { return <-events }
}

func (m *HubModel) listLen() int {
	switch m.stage {
	case stageRepos:
		return len(m.repos)
	case stageTags:
		return len(m.tags)
	default:
		return len(m.tag.Variants())
	}
}

// listHeight leaves room for title, input, header, status and help.
func (m *HubModel) listHeight() int { return max(m.height-6, 3) }

// View renders the screen.
func (m *HubModel) View() string {
	title := titleStyle.Render("Docker Hub") + subtleStyle.Render(" → pulls into "+m.where())
	switch m.stage {
	case stageTags, stagePlatforms:
		title += subtleStyle.Render(" · ") + boldStyle.Render(m.repo.Name())
	}
	if m.stage == stagePlatforms {
		title += boldStyle.Render(":" + m.tag.Name())
	}

	var body []string
	if m.stage != stagePlatforms {
		body = append(body, " "+m.input.View())
	}
	switch {
	case m.loading:
		body = append(body, helpStyle.Render("searching…"))
	case m.stage == stageRepos:
		body = append(body, m.reposView()...)
	case m.stage == stageTags:
		body = append(body, m.tagsView()...)
	default:
		body = append(body, m.platformsView()...)
	}

	status := helpStyle.Render(m.help())
	switch {
	case m.pulling:
		status = markStyle.Render(" " + m.pullText())
	case m.status != "":
		style := errStyle
		if m.ok {
			style = okStyle
		}
		status = style.Render(ansi.Truncate(m.status, m.width-2, "…"))
	}
	content := lipgloss.JoinVertical(lipgloss.Left, append([]string{title}, body...)...)
	pad := max(m.height-lipgloss.Height(content)-1, 0)
	return content + strings.Repeat("\n", pad+1) + status
}

func (m *HubModel) pullText() string {
	text := fmt.Sprintf("Pulling %s:%s… %s", m.repo.Name(), m.tag.Name(), humanize.Bytes(m.progress[0]))
	if m.progress[1] > 0 {
		text += " of " + humanize.Bytes(m.progress[1])
	}
	return text + " · " + time.Since(m.started).Round(time.Second).String()
}

func (m *HubModel) help() string {
	switch {
	case m.typing && m.stage == stageRepos:
		return "enter search • ↓ results • esc back"
	case m.typing:
		return "enter filter • ↓ tags • esc cancel"
	case m.stage == stageRepos:
		return "enter tags • / search • esc back to images"
	case m.stage == stageTags:
		return "enter choose platform • / filter • esc back"
	default:
		return "enter pull • esc back"
	}
}

// window returns the visible slice bounds around the cursor.
func (m *HubModel) window(n int) (int, int) {
	h := m.listHeight()
	c := m.cursor[m.stage]
	start := max(0, min(c-h/2, n-h))
	return start, min(start+h, n)
}

func (m *HubModel) line(i int, text string) string {
	text = ansi.Truncate(text, m.width-2, "…")
	if i == m.cursor[m.stage] && !m.typing {
		return cursorStyle.Render(padRight(text, m.width-2))
	}
	return text
}

func (m *HubModel) reposView() []string {
	if len(m.repos) == 0 {
		return []string{helpStyle.Render("Type a name and press enter.")}
	}
	nameW := 10
	for _, r := range m.repos {
		nameW = max(nameW, min(len(r.Name()), 40))
	}
	out := []string{subtleStyle.Render(fmt.Sprintf("  %-*s %8s %8s  %s", nameW, "NAME", "STARS", "PULLS", "DESCRIPTION"))}
	start, end := m.window(len(m.repos))
	for i := start; i < end; i++ {
		r := m.repos[i]
		badge := " "
		if r.Official() {
			badge = "✓"
		}
		out = append(out, m.line(i, fmt.Sprintf("%s %-*s %8s %8s  %s", badge, nameW, ansi.Truncate(r.Name(), nameW, "…"),
			humanize.Count(int64(r.Stars())), humanize.Count(r.Pulls()), r.Description())))
	}
	return out
}

func (m *HubModel) tagsView() []string {
	if len(m.tags) == 0 {
		return []string{helpStyle.Render("No tags loaded.")}
	}
	nameW := 8
	for _, t := range m.tags {
		nameW = max(nameW, min(len(t.Name()), 40))
	}
	sizeHead := "SIZE"
	if m.hasNat {
		sizeHead = "SIZE (" + m.native.Arch() + ")"
	}
	out := []string{subtleStyle.Render(fmt.Sprintf("  %-*s %-16s %12s  %s", nameW, "TAG", "UPDATED", sizeHead, "PLATFORMS"))}
	now := time.Now()
	start, end := m.window(len(m.tags))
	for i := start; i < end; i++ {
		t := m.tags[i]
		size := "-"
		if v, ok := t.Find(m.native); m.hasNat && ok {
			size = humanize.Bytes(v.Size)
		}
		updated := "-"
		if !t.Updated().IsZero() {
			updated = humanize.Since(t.Updated(), now)
		}
		out = append(out, m.line(i, fmt.Sprintf("  %-*s %-16s %12s  %s", nameW, ansi.Truncate(t.Name(), nameW, "…"),
			updated, size, platformList(t.Variants()))))
	}
	return out
}

// platformList names platforms compactly, dropping the usual linux/.
func platformList(vs []registry.Variant) string {
	names := make([]string, len(vs))
	for i, v := range vs {
		names[i] = strings.TrimPrefix(v.Platform.String(), "linux/")
	}
	return strings.Join(names, " ")
}

func (m *HubModel) platformsView() []string {
	out := []string{"", boldStyle.Render(" Pull " + m.repo.Name() + ":" + m.tag.Name() + " for:"), ""}
	for i, v := range m.tag.Variants() {
		note := ""
		if m.hasNat && v.Platform.Matches(m.native) {
			note = okStyle.Render("matches " + m.where())
		}
		out = append(out, m.line(i, fmt.Sprintf("  %-20s %10s  %s", v.Platform, humanize.Bytes(v.Size), note)))
	}
	return out
}
