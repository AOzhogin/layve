package ui

import (
	"compress/gzip"
	"fmt"
	"os"

	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/domain/registry"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

type call struct{ op, target, ref string }

type fakeService struct {
	images     []image.Image
	listErr    error
	failOn     map[string]error
	calls      []call
	history    []image.Layer
	details    []image.LayerDetail
	preview    func(image.EditPlan) ([]image.LayerDetail, error)
	inspection *fakeInspection
	written    []written
	writeErr   error
	exported   []string
	exportErr  error
	imported   []string
	closed     bool
	pulls      []string
	pullErr    error
}

func (f *fakeService) List(context.Context) ([]image.Image, error) { return f.images, f.listErr }

func (f *fakeService) Remove(_ context.Context, t image.Target, force bool) error {
	op := "rm"
	if force {
		op = "rm -f"
	}
	f.calls = append(f.calls, call{op: op, target: t.String()})
	return f.failOn[t.String()]
}

func (f *fakeService) Tag(_ context.Context, src image.Target, ref image.Reference) error {
	f.calls = append(f.calls, call{op: "tag", target: src.String(), ref: ref.String()})
	return f.failOn[ref.String()]
}

func (f *fakeService) History(context.Context, image.ID) ([]image.Layer, error) {
	return f.history, nil
}

func (f *fakeService) Inspect(_ context.Context, _ image.ID, progress func(int64)) (Inspection, error) {
	progress(10)
	f.inspection = &fakeInspection{base: f.details, preview: f.preview}
	return f.inspection, nil
}

func (f *fakeService) Write(_ context.Context, id image.ID, plan image.EditPlan, ref image.Reference, opts image.WriteOptions, progress func(string)) error {
	progress("writing layer 1/1")
	f.written = append(f.written, written{id: id, plan: plan, ref: ref, opts: opts})
	return f.writeErr
}

func (f *fakeService) Export(_ context.Context, targets []image.Target) (io.ReadCloser, error) {
	if f.exportErr != nil {
		return nil, f.exportErr
	}
	var names []string
	for _, t := range targets {
		names = append(names, t.String())
	}
	f.exported = append(f.exported, names...)
	return io.NopCloser(strings.NewReader("archive of " + strings.Join(names, ","))), nil
}

func (f *fakeService) Import(_ context.Context, r io.Reader) error {
	data, err := io.ReadAll(r)
	f.imported = append(f.imported, string(data))
	return err
}

func (f *fakeService) Platform(context.Context) (registry.Platform, error) {
	return registry.ParsePlatform("linux/amd64")
}

func (f *fakeService) Pull(_ context.Context, ref image.Reference, p registry.Platform, progress func(done, total int64)) error {
	progress(50, 100)
	progress(100, 100)
	f.pulls = append(f.pulls, ref.String()+"@"+p.String())
	return f.pullErr
}

func (f *fakeService) Close() error {
	f.closed = true
	return nil
}

// fakeConnector serves fake daemons by context name.
type fakeConnector struct {
	services map[string]*fakeService
	failOn   map[string]error
}

func (c *fakeConnector) Endpoints() ([]Endpoint, error) {
	var out []Endpoint
	for _, name := range []string{"default", "remote"} {
		if _, ok := c.services[name]; ok {
			out = append(out, Endpoint{Name: name, Address: "ssh://" + name})
		}
	}
	return out, nil
}

func (c *fakeConnector) Connect(name string) (ImageService, error) {
	if err := c.failOn[name]; err != nil {
		return nil, err
	}
	return c.services[name], nil
}

type written struct {
	id   image.ID
	plan image.EditPlan
	ref  image.Reference
	opts image.WriteOptions
}

// fakeInspection records the plans it previews. preview, when set, decides
// the result; otherwise the base details are returned unchanged.
type fakeInspection struct {
	base    []image.LayerDetail
	preview func(image.EditPlan) ([]image.LayerDetail, error)
	plans   []image.EditPlan
}

func (f *fakeInspection) Details(plan image.EditPlan) ([]image.LayerDetail, error) {
	f.plans = append(f.plans, plan)
	if f.preview != nil {
		return f.preview(plan)
	}
	return f.base, nil
}

func (f *fakeInspection) last() image.EditPlan { return f.plans[len(f.plans)-1] }

func mustRef(t *testing.T, s string) image.Reference {
	t.Helper()
	r, err := image.ParseReference(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustImage(t *testing.T, id string, tags []string, created time.Time) image.Image {
	t.Helper()
	imgID, err := image.NewID(id)
	if err != nil {
		t.Fatal(err)
	}
	refs := make([]image.Reference, 0, len(tags))
	for _, tag := range tags {
		refs = append(refs, mustRef(t, tag))
	}
	size, _ := image.NewSize(5_000_000)
	return image.New(imgID, refs, size, created)
}

func press(s string) tea.KeyPressMsg {
	switch s {
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

// drive feeds messages through the model, executing returned commands
// synchronously (batches are expanded) until nothing is left.
func drive(t *testing.T, m tea.Model, msgs ...tea.Msg) tea.Model {
	t.Helper()
	queue := msgs
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 100 {
			t.Fatalf("too many steps, last message %T %+v", queue[0], queue[0])
		}
		msg := queue[0]
		queue = queue[1:]
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		queue = append(queue, run(cmd)...)
	}
	return m
}

func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch msg := msg.(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, run(c)...)
		}
		return out
	case tea.QuitMsg:
		return nil
	default:
		return []tea.Msg{msg}
	}
}

func viewOf(m tea.Model) string { return ansi.Strip(m.(*App).View().Content) }

func newFixture(t *testing.T) (*fakeService, tea.Model) {
	now := time.Now()
	svc := &fakeService{images: []image.Image{
		mustImage(t, "sha256:aaaaaaaaaaaaaaaa", []string{"nginx:1.27", "nginx:latest"}, now.Add(-48*time.Hour)),
		mustImage(t, "sha256:bbbbbbbbbbbbbbbb", nil, now.Add(-time.Hour)),
		mustImage(t, "sha256:cccccccccccccccc", []string{"redis:7"}, now.Add(-72*time.Hour)),
	}}
	app := NewApp(svc, discard, Options{MarkHistory: true})
	m := drive(t, app, tea.WindowSizeMsg{Width: 140, Height: 30})
	m = drive(t, m, app.Init()())
	return svc, m
}

func TestBuildRows(t *testing.T) {
	_, m := newFixture(t)
	rows := m.(*App).images.rows
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.target.Display()
	}
	want := "bbbbbbbbbbbb nginx:1.27 nginx:latest redis:7"
	if strings.Join(got, " ") != want {
		t.Errorf("rows = %v, want %s", got, want)
	}
}

func TestPlanRetag(t *testing.T) {
	img := mustImage(t, "sha256:aaaa", []string{"nginx:1.27"}, time.Time{})
	nginx := row{img: img, target: image.ReferenceTarget(mustRef(t, "nginx:1.27"))}
	redis := row{img: img, target: image.ReferenceTarget(mustRef(t, "redis:7"))}
	nginx2 := row{img: img, target: image.ReferenceTarget(mustRef(t, "nginx:latest"))}
	dangling := row{img: img, target: image.IDTarget(img.ID())}

	steps, err := planRetag([]row{nginx}, "registry.local/nginx:stable")
	if err != nil || len(steps) != 1 || steps[0].to.String() != "registry.local/nginx:stable" {
		t.Fatalf("single: %v %v", steps, err)
	}
	steps, err = planRetag([]row{nginx, redis}, "v2")
	if err != nil || len(steps) != 2 || steps[1].to.String() != "redis:v2" {
		t.Fatalf("group: %v %v", steps, err)
	}
	for name, tc := range map[string]struct {
		rows  []row
		input string
	}{
		"same repo collision": {[]row{nginx, nginx2}, "v2"},
		"group full ref":      {[]row{nginx, redis}, "x:v2"},
		"group dangling":      {[]row{nginx, dangling}, "v2"},
		"empty":               {[]row{nginx}, " "},
		"unchanged":           {[]row{nginx}, "nginx:1.27"},
	} {
		if _, err := planRetag(tc.rows, tc.input); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestSelectAndDelete(t *testing.T) {
	svc, m := newFixture(t)
	// cursor on dangling row: select it, then nginx:1.27; skip nginx:latest.
	m = drive(t, m, press("space"), press("space"), press("d"))
	if !strings.Contains(viewOf(m), "Remove 2 image(s)?") {
		t.Fatalf("no confirm dialog:\n%s", viewOf(m))
	}
	svc.failOn = map[string]error{"nginx:1.27": errors.New("in use")}
	m = drive(t, m, press("y"))
	want := []call{{op: "rm", target: "sha256:bbbbbbbbbbbbbbbb"}, {op: "rm", target: "nginx:1.27"}}
	if len(svc.calls) != 2 || svc.calls[0] != want[0] || svc.calls[1] != want[1] {
		t.Errorf("calls = %v", svc.calls)
	}
	if v := viewOf(m); !strings.Contains(v, "Removed 1, failed 1") {
		t.Errorf("status missing:\n%s", v)
	}
	if n := len(m.(*App).images.selected); n != 0 {
		t.Errorf("selection not cleared: %d", n)
	}
}

func TestDeleteCancelled(t *testing.T) {
	svc, m := newFixture(t)
	m = drive(t, m, press("D"), press("n"))
	if len(svc.calls) != 0 || m.(*App).images.mode != modeList {
		t.Errorf("calls=%v mode=%v", svc.calls, m.(*App).images.mode)
	}
}

func TestRetagSingle(t *testing.T) {
	svc, m := newFixture(t)
	m = drive(t, m, press("down"), press("t"))
	if got := m.(*App).images.input.Value(); got != "nginx:1.27" {
		t.Fatalf("prefill = %q", got)
	}
	m.(*App).images.input.SetValue("nginx:stable")
	drive(t, m, press("enter"))
	want := []call{
		{op: "tag", target: "sha256:aaaaaaaaaaaaaaaa", ref: "nginx:stable"},
		{op: "rm", target: "nginx:1.27"},
	}
	if len(svc.calls) != 2 || svc.calls[0] != want[0] || svc.calls[1] != want[1] {
		t.Errorf("calls = %v", svc.calls)
	}
}

func TestSelectAllToggle(t *testing.T) {
	_, m := newFixture(t)
	m = drive(t, m, press("a"))
	if n := len(m.(*App).images.selected); n != 4 {
		t.Fatalf("selected = %d", n)
	}
	m = drive(t, m, press("a"))
	if n := len(m.(*App).images.selected); n != 0 {
		t.Errorf("selected after second a = %d", n)
	}
}

func change(t *testing.T, p string, k image.ChangeKind, dir bool) image.FileChange {
	t.Helper()
	c, err := image.NewFileChange(p, k, image.Size{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLayersScreen(t *testing.T) {
	svc, m := newFixture(t)
	base := image.NewLayer("ADD rootfs /", image.Size{}, time.Time{}, false)
	env := image.NewLayer("ENV A=1", image.Size{}, time.Time{}, true)
	app := image.NewLayer("COPY app /app", image.Size{}, time.Time{}, false)
	svc.history = []image.Layer{base, env, app}
	svc.details = []image.LayerDetail{
		image.NewLayerDetail(base, []image.FileChange{change(t, "etc/hosts", image.ChangeAdded, false)}),
		image.NewLayerDetail(env, nil),
		image.NewLayerDetail(app, []image.FileChange{
			change(t, "app", image.ChangeAdded, true),
			change(t, "app/server", image.ChangeAdded, false).
				WithMode(0o755, 1000, 1000).
				WithPreview(image.NewTextPreview("#!/bin/sh\n\texec app\x1b[31m\n")),
			change(t, "app/run", image.ChangeAdded, false).
				WithLink(image.NewSymlink("server", "app/server", false)),
			change(t, "app/gone", image.ChangeAdded, false).
				WithLink(image.NewSymlink("/missing", "missing", true)),
			change(t, "etc/hosts", image.ChangeModified, false),
			change(t, "tmp", image.ChangeDeleted, true),
		}),
	}

	m = drive(t, m, press("down"), press("enter"))
	l := m.(*App).layers
	if l == nil {
		t.Fatal("layers screen not opened")
	}
	// The background goroutine delivers progress, then details.
	for l.details == nil {
		m = drive(t, m, run(l.waitEvent())...)
	}
	v := viewOf(m)
	for _, want := range []string{"Layers of nginx:1.27", "COPY app /app", "+4 added", "~1 modified", "-1 deleted", "server", "run → server", "gone ↛ /missing"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}

	if !strings.Contains(v, "-rwxr-xr-x 1000:1000") {
		t.Errorf("attributes missing from tree:\n%s", v)
	}
	if h := lipgloss.Height(v); h > 30 {
		t.Errorf("layers view is %d lines, screen is 30", h)
	}

	// Focus files; the info pane follows the cursor.
	m = drive(t, m, press("tab"), press("down"), press("down"))
	if v := viewOf(m); !strings.Contains(v, "/app/run") || !strings.Contains(v, "resolves to /app/server") {
		t.Errorf("symlink info missing:\n%s", v)
	}
	m = drive(t, m, press("down"))
	v = viewOf(m)
	if !strings.Contains(v, "1 #!/bin/sh") || !strings.Contains(v, "2     exec app·[31m") {
		t.Errorf("preview missing or not sanitized:\n%s", v)
	}
	m = drive(t, m, press("g"))

	// Fold the "app" directory: its children disappear.
	m = drive(t, m, press("enter"))
	if strings.Contains(viewOf(m), "server") {
		t.Errorf("app/ not collapsed:\n%s", viewOf(m))
	}
	m = drive(t, m, press("enter"))
	if !strings.Contains(viewOf(m), "server") {
		t.Error("app/ not expanded again")
	}

	// Empty step.
	m = drive(t, m, press("shift+tab"), press("k"))
	if !strings.Contains(viewOf(m), "Metadata-only step") {
		t.Errorf("empty layer not explained:\n%s", viewOf(m))
	}

	m = drive(t, m, press("esc"))
	if m.(*App).layers != nil {
		t.Error("esc did not close layers")
	}
}

func TestListError(t *testing.T) {
	svc := &fakeService{listErr: errors.New("daemon down")}
	app := NewApp(svc, discard, Options{MarkHistory: true})
	m := drive(t, app, app.Init()())
	if !strings.Contains(viewOf(m), "daemon down") {
		t.Errorf("view:\n%s", viewOf(m))
	}
}

func TestViewFitsScreen(t *testing.T) {
	svc, m := newFixture(t)
	for i := 0; i < 40; i++ {
		svc.images = append(svc.images, mustImage(t, fmt.Sprintf("sha256:%016d", i), []string{fmt.Sprintf("app:%d", i)}, time.Time{}))
	}
	m = drive(t, m, press("r"), press("a"), press("d"))
	if h := lipgloss.Height(viewOf(m)); h > 30 {
		t.Errorf("confirm view is %d lines, screen is 30", h)
	}
	if !strings.Contains(viewOf(m), "[y] yes") {
		t.Error("confirm buttons cut off")
	}
	m = drive(t, m, press("n"), press("t"))
	if h := lipgloss.Height(viewOf(m)); h > 30 {
		t.Errorf("retag view is %d lines, screen is 30", h)
	}
}

func sized(t *testing.T, p string, k image.ChangeKind, size int64) image.FileChange {
	t.Helper()
	s, _ := image.NewSize(size)
	c, err := image.NewFileChange(p, k, s, false)
	if err != nil {
		t.Fatal(err)
	}
	return c.WithPreview(image.NewTextPreview("old\n")).WithMode(0o644, 0, 0)
}

// openLayers opens the layers screen of nginx:1.27 with three steps:
// base (adds etc/hosts 100B), ENV, app (rewrites etc/hosts, adds app/).
func openLayers(t *testing.T) (*fakeService, tea.Model) {
	t.Helper()
	svc, m := newFixture(t)
	base := image.NewLayer("ADD rootfs /", image.Size{}, time.Time{}, false)
	env := image.NewLayer("ENV A=1", image.Size{}, time.Time{}, true)
	app := image.NewLayer("COPY app /app", image.Size{}, time.Time{}, false)
	svc.history = []image.Layer{base, env, app}
	svc.details = []image.LayerDetail{
		image.NewLayerDetail(base, []image.FileChange{sized(t, "etc/hosts", image.ChangeAdded, 100)}),
		image.NewLayerDetail(env, nil),
		image.NewLayerDetail(app, []image.FileChange{
			change(t, "app", image.ChangeAdded, true),
			sized(t, "app/server", image.ChangeAdded, 10),
			sized(t, "etc/hosts", image.ChangeModified, 120),
		}),
	}
	m = drive(t, m, press("down"), press("enter"))
	for l := m.(*App).layers; l.details == nil; {
		m = drive(t, m, run(l.waitEvent())...)
	}
	return svc, m
}

func lastEdits(svc *fakeService) []string {
	var out []string
	for _, e := range svc.inspection.last().Edits() {
		out = append(out, e.String())
	}
	return out
}

func TestMarkAndMergeLayers(t *testing.T) {
	svc, m := openLayers(t)
	// Cursor starts on the last step (3). Mark it, go to step 1, mark, merge.
	m = drive(t, m, press("space"), press("g"), press("space"), press("m"))
	if got := lastEdits(svc); len(got) != 1 || got[0] != "merge layers 1–3" {
		t.Fatalf("edits = %v", got)
	}
	v := viewOf(m)
	if !strings.Contains(v, "1 pending edits") || !strings.Contains(v, "merged layers 1–3") {
		t.Errorf("view:\n%s", v)
	}
	m = drive(t, m, press("u"))
	if svc.inspection.last().Len() != 0 || !strings.Contains(viewOf(m), "0 pending edits") {
		t.Errorf("undo did not clear the plan: %v", lastEdits(svc))
	}
}

func TestMarkMetadataStepRefused(t *testing.T) {
	_, m := openLayers(t)
	m = drive(t, m, press("k"), press("space"))
	if !strings.Contains(viewOf(m), "metadata-only step") {
		t.Errorf("view:\n%s", viewOf(m))
	}
}

func TestDropLayerWarnsAboutDependents(t *testing.T) {
	svc, m := openLayers(t)
	m = drive(t, m, press("g"), press("x"))
	v := viewOf(m)
	if !strings.Contains(v, "Drop layer 1?") || !strings.Contains(v, "/etc/hosts (modified in step 3)") {
		t.Fatalf("no dependency warning:\n%s", v)
	}
	drive(t, m, press("y"))
	if got := lastEdits(svc); len(got) != 1 || got[0] != "drop layer 1" {
		t.Errorf("edits = %v", got)
	}
}

func TestFileEdits(t *testing.T) {
	svc, m := openLayers(t)
	// Tree of step 3: app/, app/server, etc/, etc/hosts. Go to app/server.
	m = drive(t, m, press("tab"), press("down"), press("d"))
	if got := lastEdits(svc); len(got) != 1 || got[0] != "remove /app/server from layer 3" {
		t.Fatalf("edits = %v", got)
	}
	m = drive(t, m, press("D"))
	if !strings.Contains(viewOf(m), "Purge /app/server from every layer?") {
		t.Fatalf("no purge confirm:\n%s", viewOf(m))
	}
	drive(t, m, press("y"))
	if got := lastEdits(svc); len(got) != 2 || got[1] != "purge /app/server from image" {
		t.Errorf("edits = %v", got)
	}
}

func TestPreviewErrorKeepsPlan(t *testing.T) {
	svc, m := openLayers(t)
	svc.inspection.preview = func(p image.EditPlan) ([]image.LayerDetail, error) {
		if p.Len() > 0 {
			return nil, errors.New("/bin/sh is a hard link to /bin/busybox, which no longer exists")
		}
		return svc.details, nil
	}
	m = drive(t, m, press("tab"), press("down"), press("d"))
	if !strings.Contains(viewOf(m), "hard link") {
		t.Errorf("error not shown:\n%s", viewOf(m))
	}
	if m.(*App).layers.plan.Len() != 0 {
		t.Error("failed edit kept in plan")
	}
}

func TestWastedFix(t *testing.T) {
	svc, m := openLayers(t)
	v := viewOf(m)
	if !strings.Contains(v, "wasted 100B in 1 files") {
		t.Fatalf("no wasted summary:\n%s", v)
	}
	m = drive(t, m, press("W"))
	if !strings.Contains(viewOf(m), "/etc/hosts") || !strings.Contains(viewOf(m), "step 3") {
		t.Fatalf("wasted list:\n%s", viewOf(m))
	}
	drive(t, m, press("f"))
	if got := lastEdits(svc); len(got) != 1 || got[0] != "remove /etc/hosts from layer 1" {
		t.Errorf("edits = %v", got)
	}
}

func TestEditorRoundTrip(t *testing.T) {
	svc, m := openLayers(t)
	tmp := t.TempDir() + "/hosts"
	if err := os.WriteFile(tmp, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := m.(*App).layers
	drive(t, m, editDoneMsg{session: l.session, step: 2, path: "etc/hosts", tmp: tmp, original: []byte("old\n"), mode: 0o644})
	if got := lastEdits(svc); len(got) != 1 || got[0] != "write /etc/hosts in layer 3" {
		t.Fatalf("edits = %v", got)
	}
	content, _, _, _, _, _ := svc.inspection.last().Edits()[0].File()
	if string(content) != "new\n" {
		t.Errorf("content = %q", content)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("temp file not removed")
	}
}

func TestAddFile(t *testing.T) {
	svc, m := openLayers(t)
	host := t.TempDir() + "/app.conf"
	if err := os.WriteFile(host, []byte("key=1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// Cursor on app/ in step 3.
	m = drive(t, m, press("tab"), press("a"))
	m.(*App).layers.dialog.input.SetValue(host)
	m = drive(t, m, press("enter"))
	if got := m.(*App).layers.dialog.input.Value(); got != "/app/app.conf" {
		t.Fatalf("destination prefill = %q", got)
	}
	drive(t, m, press("enter"))
	got := svc.inspection.last().Edits()
	if len(got) != 1 || got[0].String() != "write /app/app.conf in layer 3" {
		t.Fatalf("edits = %v", got)
	}
	if content, _, mode, _, _, _ := got[0].File(); string(content) != "key=1\n" || mode != 0o640 {
		t.Errorf("content=%q mode=%v", content, mode)
	}
}

func TestWriteImage(t *testing.T) {
	svc, m := openLayers(t)
	m = drive(t, m, press("w"))
	if !strings.Contains(viewOf(m), "no pending edits") {
		t.Fatalf("write without edits:\n%s", viewOf(m))
	}
	m = drive(t, m, press("tab"), press("down"), press("d"), press("w"))
	if got := m.(*App).layers.dialog.input.Value(); got != "nginx:1.27-edited" {
		t.Fatalf("default target = %q", got)
	}
	m = drive(t, m, press("enter"))
	if len(svc.written) != 1 || svc.written[0].ref.String() != "nginx:1.27-edited" || svc.written[0].plan.Len() != 1 {
		t.Fatalf("written = %+v", svc.written)
	}
	if m.(*App).layers != nil {
		t.Error("layers screen still open after write")
	}
	if !strings.Contains(viewOf(m), "Saved as nginx:1.27-edited") {
		t.Errorf("view:\n%s", viewOf(m))
	}
}

func TestWriteFailureStays(t *testing.T) {
	svc, m := openLayers(t)
	svc.writeErr = errors.New("no space left on device")
	m = drive(t, m, press("tab"), press("down"), press("d"), press("w"), press("enter"))
	if m.(*App).layers == nil || !strings.Contains(viewOf(m), "write failed: no space left") {
		t.Errorf("view:\n%s", viewOf(m))
	}
}

func TestEscWithPendingEditsAsks(t *testing.T) {
	_, m := openLayers(t)
	m = drive(t, m, press("tab"), press("down"), press("d"), press("esc"))
	if m.(*App).layers == nil || !strings.Contains(viewOf(m), "Discard 1 pending edit(s)?") {
		t.Fatalf("view:\n%s", viewOf(m))
	}
	m = drive(t, m, press("y"))
	if m.(*App).layers != nil {
		t.Error("layers screen not closed")
	}
}

func TestGlobalKeysWorkInWastedList(t *testing.T) {
	svc, m := openLayers(t)
	m = drive(t, m, press("W"), press("f"), press("w"))
	if m.(*App).layers.dialog.kind != dialogInput {
		t.Fatalf("w did not open the write dialog:\n%s", viewOf(m))
	}
	m = drive(t, m, press("esc"), press("u"))
	if svc.inspection.last().Len() != 0 {
		t.Error("u did not undo from the wasted list")
	}
	m = drive(t, m, press("esc"))
	if m.(*App).layers == nil || m.(*App).layers.showWasted {
		t.Error("esc should leave the wasted list first, not the screen")
	}
}

func TestWriteHistoryMarkToggle(t *testing.T) {
	svc, m := openLayers(t)
	m = drive(t, m, press("tab"), press("down"), press("d"), press("w"))
	if !strings.Contains(viewOf(m), `"edited with layve": on`) {
		t.Fatalf("mark state not shown:\n%s", viewOf(m))
	}
	m = drive(t, m, press("tab"))
	if !strings.Contains(viewOf(m), `"edited with layve": off`) {
		t.Fatalf("tab did not toggle:\n%s", viewOf(m))
	}
	drive(t, m, press("enter"))
	if len(svc.written) != 1 || svc.written[0].opts.MarkHistory {
		t.Errorf("written = %+v", svc.written)
	}
}

func contextFixture(t *testing.T) (*fakeService, *fakeService, *fakeConnector, tea.Model) {
	t.Helper()
	local, _ := newFixture(t)
	remote := &fakeService{images: []image.Image{
		mustImage(t, "sha256:dddddddddddddddd", []string{"prod/api:7"}, time.Now()),
	}}
	conn := &fakeConnector{services: map[string]*fakeService{"default": local, "remote": remote}}
	app := NewApp(local, discard, Options{MarkHistory: true, Connector: conn, Context: "default"})
	m := drive(t, app, tea.WindowSizeMsg{Width: 140, Height: 30})
	m = drive(t, m, app.Init()())
	return local, remote, conn, m
}

func TestSwitchContext(t *testing.T) {
	local, _, _, m := contextFixture(t)
	if !strings.Contains(viewOf(m), "Images @ default") {
		t.Fatalf("context not in header:\n%s", viewOf(m))
	}
	m = drive(t, m, press("c"))
	v := viewOf(m)
	if !strings.Contains(v, "Switch Docker context") || !strings.Contains(v, "● default") || !strings.Contains(v, "remote   ssh://remote ") {
		t.Fatalf("picker:\n%s", v)
	}
	m = drive(t, m, press("down"), press("enter"))
	v = viewOf(m)
	if !strings.Contains(v, "Images @ remote") || !strings.Contains(v, "prod/api") || strings.Contains(v, "nginx") {
		t.Errorf("not switched:\n%s", v)
	}
	if !local.closed {
		t.Error("old connection not closed")
	}
}

func TestSwitchContextError(t *testing.T) {
	_, _, conn, m := contextFixture(t)
	conn.failOn = map[string]error{"remote": errors.New("bad ssh address")}
	m = drive(t, m, press("c"))
	m = drive(t, m, press("down"), press("enter"))
	v := viewOf(m)
	if !strings.Contains(v, "context remote: bad ssh address") || !strings.Contains(v, "Images @ default") {
		t.Errorf("view:\n%s", v)
	}
}

func TestCopyToContext(t *testing.T) {
	local, remote, _, m := contextFixture(t)
	// Rows: dangling, nginx:1.27, nginx:latest, redis:7. Select two nginx tags.
	m = drive(t, m, press("down"), press("space"), press("space"))
	m = drive(t, m, press("y"))
	v := viewOf(m)
	if !strings.Contains(v, "Copy 2 image(s) from default to") || strings.Contains(v, "● default") {
		t.Fatalf("copy picker should list only other contexts:\n%s", v)
	}
	m = drive(t, m, press("enter"))
	if got := strings.Join(local.exported, " "); got != "nginx:1.27 nginx:latest" {
		t.Errorf("exported = %q", got)
	}
	if len(remote.imported) != 1 || remote.imported[0] != "archive of nginx:1.27,nginx:latest" {
		t.Errorf("imported = %q", remote.imported)
	}
	if !strings.Contains(viewOf(m), "Copied 2 image(s) to remote") {
		t.Errorf("view:\n%s", viewOf(m))
	}
	if !remote.closed {
		t.Error("target connection not closed after copy")
	}
}

func TestCopyFailure(t *testing.T) {
	local, _, _, m := contextFixture(t)
	local.exportErr = errors.New("ssh: Permission denied (publickey)")
	m = drive(t, m, press("y"))
	m = drive(t, m, press("enter"))
	if !strings.Contains(viewOf(m), "copy to remote failed: ssh: Permission denied") {
		t.Errorf("view:\n%s", viewOf(m))
	}
}

func TestSaveImages(t *testing.T) {
	svc, m := newFixture(t)
	dir := t.TempDir()
	m = drive(t, m, press("down"), press("s"))
	if got := m.(*App).images.input.Value(); got != "nginx_1.27.tar" {
		t.Fatalf("default name = %q", got)
	}
	m.(*App).images.input.SetValue(dir + "/nginx.tar")
	m = drive(t, m, press("enter"))
	data, err := os.ReadFile(dir + "/nginx.tar")
	if err != nil || string(data) != "archive of nginx:1.27" {
		t.Fatalf("file = %q, %v", data, err)
	}
	if !strings.Contains(viewOf(m), "Saved 1 image(s) to "+dir+"/nginx.tar") {
		t.Errorf("view:\n%s", viewOf(m))
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}

	// Existing files are not overwritten.
	m = drive(t, m, press("s"))
	m.(*App).images.input.SetValue(dir + "/nginx.tar")
	m = drive(t, m, press("enter"))
	if !strings.Contains(viewOf(m), "already exists") || len(svc.exported) != 1 {
		t.Errorf("view:\n%s", viewOf(m))
	}
}

func TestSaveCompressed(t *testing.T) {
	_, m := newFixture(t)
	path := t.TempDir() + "/all.tar.gz"
	m = drive(t, m, press("a"), press("s"))
	if got := m.(*App).images.input.Value(); got != "images.tar" {
		t.Fatalf("default name for several = %q", got)
	}
	m.(*App).images.input.SetValue(path)
	drive(t, m, press("enter"))
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	data, _ := io.ReadAll(zr)
	if !strings.HasPrefix(string(data), "archive of ") || strings.Count(string(data), ",") != 3 {
		t.Errorf("content = %q", data)
	}
}

func TestLoadImages(t *testing.T) {
	svc, m := newFixture(t)
	path := t.TempDir() + "/in.tar"
	if err := os.WriteFile(path, []byte("tarball"), 0o644); err != nil {
		t.Fatal(err)
	}
	m = drive(t, m, press("l"))
	m.(*App).images.input.SetValue(path)
	svc.images = append(svc.images, mustImage(t, "sha256:eeeeeeeeeeeeeeee", []string{"loaded:1"}, time.Now()))
	m = drive(t, m, press("enter"))
	if len(svc.imported) != 1 || svc.imported[0] != "tarball" {
		t.Fatalf("imported = %q", svc.imported)
	}
	v := viewOf(m)
	if !strings.Contains(v, "Loaded in.tar") || !strings.Contains(v, "loaded") {
		t.Errorf("list not refreshed after load:\n%s", v)
	}
	m = drive(t, m, press("l"))
	m.(*App).images.input.SetValue(path + ".missing")
	m = drive(t, m, press("enter"))
	if !strings.Contains(viewOf(m), "no such file") {
		t.Errorf("view:\n%s", viewOf(m))
	}
}

type fakeHub struct {
	queries []string
	filters []string
	err     error
}

func (h *fakeHub) Search(_ context.Context, q string) ([]registry.Repository, error) {
	h.queries = append(h.queries, q)
	if h.err != nil {
		return nil, h.err
	}
	a, _ := registry.NewRepository("nginx", "Official build of Nginx.", 21425, 13_616_996_008, true)
	b, _ := registry.NewRepository("nginx/nginx-ingress", "Ingress controller", 124, 1_088_881_702, false)
	return []registry.Repository{a, b}, nil
}

func (h *fakeHub) Tags(_ context.Context, repo, filter string) ([]registry.Tag, error) {
	h.filters = append(h.filters, repo+"|"+filter)
	amd, _ := registry.ParsePlatform("linux/amd64")
	arm, _ := registry.ParsePlatform("linux/arm64/v8")
	t1, _ := registry.NewTag("1.27-alpine", time.Now().Add(-48*time.Hour), []registry.Variant{{Platform: arm, Size: 20_000_000}, {Platform: amd, Size: 21_000_000}})
	t2, _ := registry.NewTag("1.27", time.Now().Add(-72*time.Hour), []registry.Variant{{Platform: amd, Size: 70_000_000}})
	return []registry.Tag{t1, t2}, nil
}

func hubFixture(t *testing.T) (*fakeService, *fakeHub, tea.Model) {
	t.Helper()
	svc, _ := newFixture(t)
	h := &fakeHub{}
	app := NewApp(svc, discard, Options{Hub: h, Context: "default"})
	m := drive(t, app, tea.WindowSizeMsg{Width: 140, Height: 30})
	m = drive(t, m, app.Init()())
	m = drive(t, m, press("/"))
	return svc, h, m
}

func typeText(t *testing.T, m tea.Model, s string) tea.Model {
	t.Helper()
	for _, r := range s {
		m = drive(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

func TestHubSearchAndPull(t *testing.T) {
	svc, h, m := hubFixture(t)
	if !strings.Contains(viewOf(m), "→ pulls into default") {
		t.Fatalf("hub screen:\n%s", viewOf(m))
	}
	m = typeText(t, m, "nginx")
	m = drive(t, m, press("enter"))
	v := viewOf(m)
	if len(h.queries) != 1 || h.queries[0] != "nginx" || !strings.Contains(v, "✓ nginx") || !strings.Contains(v, "13.6B") {
		t.Fatalf("results:\n%s", v)
	}
	m = drive(t, m, press("enter"))
	v = viewOf(m)
	if h.filters[0] != "nginx|" || !strings.Contains(v, "1.27-alpine") || !strings.Contains(v, "amd64 arm64/v8") || !strings.Contains(v, "SIZE (amd64)") || !strings.Contains(v, "21MB") {
		t.Fatalf("tags:\n%s", v)
	}
	// Platform list: the daemon's own platform is preselected.
	m = drive(t, m, press("enter"))
	if c := m.(*App).hub.cursor[stagePlatforms]; c != 0 {
		t.Errorf("cursor on %d, want linux/amd64 (0)", c)
	}
	if !strings.Contains(viewOf(m), "matches default") {
		t.Errorf("platforms:\n%s", viewOf(m))
	}
	m = drive(t, m, press("down"), press("enter"))
	if len(svc.pulls) != 1 || svc.pulls[0] != "nginx:1.27-alpine@linux/arm64/v8" {
		t.Fatalf("pulls = %v", svc.pulls)
	}
	v = viewOf(m)
	if !strings.Contains(v, "Pulled nginx:1.27-alpine for linux/arm64/v8") || !strings.Contains(v, "will not run on this linux/amd64 machine") {
		t.Errorf("status:\n%s", v)
	}
	// Back to the image list, which reloads after a pull.
	m = drive(t, m, press("esc"), press("esc"), press("esc"))
	if m.(*App).hub != nil || !strings.Contains(viewOf(m), "Images @ default") {
		t.Errorf("not back on the list:\n%s", viewOf(m))
	}
}

func TestHubTagFilterAndErrors(t *testing.T) {
	svc, h, m := hubFixture(t)
	m = typeText(t, m, "nginx")
	m = drive(t, m, press("enter"))
	m = drive(t, m, press("enter"))
	m = drive(t, m, press("/"))
	m = typeText(t, m, "1.27")
	m = drive(t, m, press("enter"))
	if last := h.filters[len(h.filters)-1]; last != "nginx|1.27" {
		t.Errorf("filter = %q", last)
	}
	svc.pullErr = errors.New("no matching manifest for linux/s390x")
	m = drive(t, m, press("enter"))
	m = drive(t, m, press("enter"))
	if !strings.Contains(viewOf(m), "no matching manifest") {
		t.Errorf("pull error not shown:\n%s", viewOf(m))
	}

	_, h2, m2 := hubFixture(t)
	h2.err = errors.New("docker hub: rate limit reached, try again in a minute")
	m2 = typeText(t, m2, "x")
	m2 = drive(t, m2, press("enter"))
	if !strings.Contains(viewOf(m2), "rate limit reached") {
		t.Errorf("search error not shown:\n%s", viewOf(m2))
	}
}

func TestMain(m *testing.M) {
	cursorBlink = false
	os.Exit(m.Run())
}

func TestTransferBusyMessage(t *testing.T) {
	_, m := newFixture(t)
	m.(*App).images.transfer.active = true
	m = drive(t, m, press("s"))
	if !strings.Contains(viewOf(m), "wait for the current transfer to finish") {
		t.Errorf("busy message not shown:\n%s", viewOf(m))
	}
}
