package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AOzhogin/layve/internal/domain/image"
)

type fakeService struct {
	img      image.Image
	in       *fakeInspection
	written  []image.Reference
	plans    []image.EditPlan
	opts     []image.WriteOptions
	findErr  error
	contexts []string
}

func (f *fakeService) Find(_ context.Context, name string) (image.Image, error) {
	if f.findErr != nil {
		return image.Image{}, f.findErr
	}
	return f.img, nil
}

func (f *fakeService) Inspect(context.Context, image.ID, func(int64)) (Inspection, error) {
	return f.in, nil
}

func (f *fakeService) Write(_ context.Context, _ image.ID, plan image.EditPlan, ref image.Reference, opts image.WriteOptions, progress func(string)) error {
	progress("writing layer 1/1")
	f.written = append(f.written, ref)
	f.plans = append(f.plans, plan)
	f.opts = append(f.opts, opts)
	return nil
}

func (f *fakeService) Close() error { return nil }

// fakeInspection returns base details for an empty plan and after for any
// other; reject fails plans containing a matching edit.
type fakeInspection struct {
	base, after []image.LayerDetail
	reject      func(image.Edit) bool
}

func (f *fakeInspection) Details(p image.EditPlan) ([]image.LayerDetail, error) {
	for _, e := range p.Edits() {
		if f.reject != nil && f.reject(e) {
			return nil, errors.New("hard link target")
		}
	}
	if p.Len() == 0 {
		return f.base, nil
	}
	return f.after, nil
}

func change(t *testing.T, p string, k image.ChangeKind, size int64) image.FileChange {
	t.Helper()
	s, _ := image.NewSize(size)
	c, err := image.NewFileChange(p, k, s, false)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func layer(cmd string, size int64, empty bool, changes ...image.FileChange) image.LayerDetail {
	s, _ := image.NewSize(size)
	return image.NewLayerDetail(image.NewLayer(cmd, s, time.Time{}, empty), changes)
}

func fixture(t *testing.T) *fakeService {
	t.Helper()
	id, _ := image.NewID("sha256:5650e357c24959096d0c")
	ref, _ := image.ParseReference("example/webapp:1.0")
	size, _ := image.NewSize(51_200_000)
	base := []image.LayerDetail{
		layer("ADD rootfs /", 7_800_000, false, change(t, "etc/app.conf", image.ChangeAdded, 100)),
		layer("ENV A=1", 0, true),
		layer("RUN make", 3_000_000, false, change(t, "app/node_modules.tar", image.ChangeAdded, 3_000_000)),
		layer("RUN rm", 200, false,
			change(t, "app/node_modules.tar", image.ChangeDeleted, 0),
			change(t, "etc/app.conf", image.ChangeModified, 120)),
	}
	after := []image.LayerDetail{
		layer("ADD rootfs /", 7_800_000, false),
		layer("ENV A=1", 0, true),
		layer("RUN make", 0, false),
		layer("RUN rm", 200, false, change(t, "etc/app.conf", image.ChangeModified, 120)),
	}
	return &fakeService{
		img: image.New(id, []image.Reference{ref}, size, time.Now()),
		in:  &fakeInspection{base: base, after: after},
	}
}

func run(t *testing.T, svc *fakeService, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	connect := func(name string) (Service, error) {
		svc.contexts = append(svc.contexts, name)
		return svc, nil
	}
	code := Run(args, connect, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestIsCommand(t *testing.T) {
	if !IsCommand("fix") || IsCommand("--no-mark") || IsCommand("serve") {
		t.Error("IsCommand")
	}
}

func TestInspect(t *testing.T) {
	code, out, _ := run(t, fixture(t), "inspect", "example/webapp:1.0")
	if code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{"example/webapp:1.0 (5650e357c249): 3 layers", "wasted 3MB in 2 files", "RUN make", "/app/node_modules.tar"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestInspectJSON(t *testing.T) {
	code, out, _ := run(t, fixture(t), "inspect", "--json", "example/webapp:1.0", "--context", "prod")
	if code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	var got struct {
		Layers int `json:"layers"`
		Steps  []struct {
			Step  int  `json:"step"`
			Empty bool `json:"empty"`
		} `json:"steps"`
		Wasted struct {
			Bytes int64 `json:"bytes"`
			Files int   `json:"files"`
		} `json:"wasted"`
		Files []struct {
			Path     string `json:"path"`
			Step     int    `json:"step"`
			HiddenBy int    `json:"hidden_by"`
		} `json:"wasted_files"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got.Layers != 3 || len(got.Steps) != 4 || !got.Steps[1].Empty || got.Wasted.Files != 2 {
		t.Errorf("got %+v", got)
	}
	if got.Files[0].Path != "/app/node_modules.tar" || got.Files[0].Step != 3 || got.Files[0].HiddenBy != 4 {
		t.Errorf("files = %+v", got.Files)
	}
}

func TestCheck(t *testing.T) {
	svc := fixture(t)
	if code, out, _ := run(t, svc, "check", "example/webapp:1.0", "--max-wasted", "5MB"); code != ExitOK || !strings.Contains(out, "— ok") {
		t.Errorf("under limit: %d\n%s", code, out)
	}
	code, out, _ := run(t, svc, "check", "example/webapp:1.0", "--max-wasted", "1MB", "--max-layers", "2")
	if code != ExitCheckFailed {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{"FAILED", "wasted 3MB exceeds 1MB", "3 layers exceed 2", "layve fix example/webapp:1.0 --wasted"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if code, _, errOut := run(t, svc, "check", "x:1"); code != ExitError || !strings.Contains(errOut, "--max-wasted") {
		t.Errorf("no limits: %d %s", code, errOut)
	}
	if code, _, errOut := run(t, svc, "check", "x:1", "--max-wasted", "lots"); code != ExitError || !strings.Contains(errOut, "invalid size") {
		t.Errorf("bad size: %d %s", code, errOut)
	}
}

func TestFixOverwritesTagByDefault(t *testing.T) {
	svc := fixture(t)
	code, out, errOut := run(t, svc, "fix", "example/webapp:1.0", "--wasted")
	if code != ExitOK {
		t.Fatalf("code = %d, %s", code, errOut)
	}
	if len(svc.written) != 1 || svc.written[0].String() != "example/webapp:1.0" || !svc.opts[0].MarkHistory {
		t.Fatalf("written = %v %v", svc.written, svc.opts)
	}
	edits := describe(svc.plans[0])
	if strings.Join(edits, "; ") != "remove /app/node_modules.tar from layer 3; remove /etc/app.conf from layer 1" {
		t.Errorf("edits = %v", edits)
	}
	for _, want := range []string{"remove 2 hidden file versions (3MB)", "before: 3 layers, 10.8MB, wasted 3MB in 2 files", "after:  3 layers, 7.8MB, wasted 0B in 0 files", "previous image is kept as 5650e357c249"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "writing layer 1/1") {
		t.Errorf("no progress on stderr: %q", errOut)
	}
}

func TestFixPlanOrder(t *testing.T) {
	svc := fixture(t)
	code, _, errOut := run(t, svc, "fix", "--drop", "1", "--purge", "/root/.npmrc", "--merge", "3-4", "--drop", "3",
		"-o", "example/webapp:fixed", "--no-mark", "example/webapp:1.0")
	if code != ExitOK {
		t.Fatalf("code = %d, %s", code, errOut)
	}
	got := strings.Join(describe(svc.plans[0]), "; ")
	want := "merge layers 3–4; drop layer 3; drop layer 1; purge /root/.npmrc from image"
	if got != want {
		t.Errorf("plan = %s\nwant   %s", got, want)
	}
	if svc.written[0].String() != "example/webapp:fixed" || svc.opts[0].MarkHistory {
		t.Errorf("written %v, opts %v", svc.written, svc.opts)
	}
}

func TestFixSquash(t *testing.T) {
	svc := fixture(t)
	if code, _, errOut := run(t, svc, "fix", "example/webapp:1.0", "--squash", "--dry-run"); code != ExitOK {
		t.Fatalf("code = %d %s", code, errOut)
	}
	if len(svc.written) != 0 {
		t.Error("dry run wrote an image")
	}
	code, out, _ := run(t, svc, "fix", "example/webapp:1.0", "--squash", "--json")
	if code != ExitOK {
		t.Fatal(code)
	}
	var got struct {
		Edits   []string `json:"edits"`
		Written bool     `json:"written"`
		Before  struct {
			Wasted struct{ Bytes int64 } `json:"wasted"`
		} `json:"before"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(got.Edits) != 1 || got.Edits[0] != "merge layers 1–4" || !got.Written || got.Before.Wasted.Bytes != 3_000_100 {
		t.Errorf("got %+v", got)
	}
}

func TestFixWastedSkipsHardLinkTargets(t *testing.T) {
	svc := fixture(t)
	svc.in.reject = func(e image.Edit) bool { return e.Path() == "etc/app.conf" }
	code, out, _ := run(t, svc, "fix", "example/webapp:1.0", "--wasted", "--dry-run")
	if code != ExitOK || !strings.Contains(out, "remove 1 of 2 hidden file versions") {
		t.Errorf("%d\n%s", code, out)
	}
}

func TestFixErrors(t *testing.T) {
	svc := fixture(t)
	cases := map[string][]string{
		"nothing to do":                 {"fix", "example/webapp:1.0"},
		"want A-B":                      {"fix", "example/webapp:1.0", "--merge", "3"},
		"want a step number":            {"fix", "example/webapp:1.0", "--drop", "9"},
		"expected exactly one":          {"fix", "--wasted"},
		"flag provided but not defined": {"fix", "example/webapp:1.0", "--bogus"},
	}
	for want, args := range cases {
		code, _, errOut := run(t, svc, args...)
		if code != ExitError || !strings.Contains(errOut, want) {
			t.Errorf("%v: code %d, stderr %q", args, code, errOut)
		}
	}
	svc.findErr = errors.New("No such image: nope:1")
	if code, _, errOut := run(t, svc, "fix", "nope:1", "--wasted"); code != ExitError || !strings.Contains(errOut, "No such image") {
		t.Errorf("missing image: %d %s", code, errOut)
	}
}

func TestFixTargetByID(t *testing.T) {
	img := fixture(t).img
	if ref, err := fixTarget(img, "5650e357c249", ""); err != nil || ref.String() != "example/webapp:1.0" {
		t.Errorf("by id: %v %v", ref, err)
	}
	two := image.New(img.ID(), append(img.References(), mustRef(t, "example/webapp:latest")), img.Size(), img.Created())
	if _, err := fixTarget(two, "5650e357c249", ""); err == nil {
		t.Error("ambiguous tag accepted")
	}
	if ref, err := fixTarget(two, "example/webapp:latest", ""); err != nil || ref.Tag() != "latest" {
		t.Errorf("by tag: %v %v", ref, err)
	}
}

func mustRef(t *testing.T, s string) image.Reference {
	t.Helper()
	r, err := image.ParseReference(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHelpExitsZero(t *testing.T) {
	code, _, errOut := run(t, fixture(t), "fix", "--help")
	if code != ExitOK || !strings.Contains(errOut, "-wasted") {
		t.Errorf("%d %s", code, errOut)
	}
}
