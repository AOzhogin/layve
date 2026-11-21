package imagearchive

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
)

// finalFS applies all layers and returns path -> content ("" for dirs).
func finalFS(t *testing.T, layers []Layer) map[string]string {
	t.Helper()
	fsys := map[string]string{}
	for _, l := range layers {
		for _, e := range l.Entries {
			switch {
			case e.Whiteout:
				for p := range fsys {
					if p == e.Path || strings.HasPrefix(p, e.Path+"/") {
						delete(fsys, p)
					}
				}
			case e.Opaque:
				for p := range fsys {
					if strings.HasPrefix(p, e.Path+"/") {
						delete(fsys, p)
					}
				}
			}
		}
		for _, e := range l.Entries {
			if e.Whiteout || e.Opaque {
				continue
			}
			fsys[e.Path] = string(e.Content)
		}
	}
	return fsys
}

// opaque markers must not delete entries of their own layer, so apply them
// before the layer's entries: finalFS does exactly that.

func keys(m map[string]string) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func txt(p, body string) Entry {
	return Entry{Path: p, Size: int64(len(body)), Mode: 0o644, Preview: PreviewText, Content: []byte(body)}
}

func dir(p string) Entry { return Entry{Path: p, Dir: true, Mode: 0o755 | 1<<31} }

func testArchive() *Archive {
	return &Archive{
		History: []History{
			{CreatedBy: "base"},
			{CreatedBy: "ENV A=1", EmptyLayer: true},
			{CreatedBy: "add config"},
			{CreatedBy: "replace config"},
			{CreatedBy: "cleanup"},
		},
		Layers: []Layer{
			{Entries: []Entry{dir("etc"), txt("etc/app.conf", "v1"), dir("tmp"), txt("tmp/a", "a"), dir("var"), txt("var/log", "l")}},
			{Entries: []Entry{txt("etc/app.conf", "v2"), {Path: "tmp", Whiteout: true}}},
			{Entries: []Entry{txt("etc/app.conf", "v3"), dir("tmp"), txt("tmp/b", "b")}},
			{Entries: []Entry{{Path: "var/log", Whiteout: true}, txt("etc/extra", "x")}},
		},
	}
}

func TestStepsAfterMergeAndDrop(t *testing.T) {
	s := NewState(testArchive())
	if err := s.MergeLayers(2, 3); err != nil {
		t.Fatal(err)
	}
	steps := s.Steps()
	if len(steps) != 5 || !steps[2].EmptyLayer || steps[3].Layer != 1 || steps[3].Merged != 2 || !steps[3].Edited {
		t.Fatalf("steps after merge = %+v", steps)
	}
	if err := s.DropLayer(4); err != nil {
		t.Fatal(err)
	}
	steps = s.Steps()
	if len(steps) != 4 || steps[3].CreatedBy != "replace config" {
		t.Fatalf("steps after drop = %+v", steps)
	}
	if err := s.DropLayer(1); err == nil {
		t.Error("dropping a metadata-only step should fail")
	}
	if err := s.MergeLayers(0, 1); err == nil {
		t.Error("merging a single layer should fail")
	}
}

func TestMergeKeepsFinalFilesystem(t *testing.T) {
	a := testArchive()
	want := finalFS(t, a.Layers)
	for _, rng := range [][2]int{{2, 3}, {0, 3}, {3, 4}, {0, 4}} {
		s := NewState(a)
		if err := s.MergeLayers(rng[0], rng[1]); err != nil {
			t.Fatal(err)
		}
		got := finalFS(t, s.Layers())
		if keys(got) != keys(want) || got["etc/app.conf"] != "v3" {
			t.Errorf("merge %v: fs = %v, want %v", rng, got, want)
		}
	}
}

func TestMergeEmitsMarkers(t *testing.T) {
	s := NewState(testArchive())
	_ = s.MergeLayers(2, 3) // whiteout tmp, then re-add tmp/b
	var opaque, wh []string
	for _, e := range s.Layers()[1].Entries {
		if e.Opaque {
			opaque = append(opaque, e.Path)
		}
		if e.Whiteout {
			wh = append(wh, e.Path)
		}
	}
	if len(opaque) != 1 || opaque[0] != "tmp" || len(wh) != 0 {
		t.Errorf("opaque=%v whiteouts=%v", opaque, wh)
	}
}

func TestRemovePathRevealsLowerVersion(t *testing.T) {
	s := NewState(testArchive())
	// Undo "replace config" for app.conf: v2 from step 3 shows through.
	if err := s.RemovePath(3, "etc/app.conf"); err != nil {
		t.Fatal(err)
	}
	if got := finalFS(t, s.Layers())["etc/app.conf"]; got != "v2" {
		t.Errorf("app.conf = %q, want v2", got)
	}
	if !s.Steps()[3].Edited {
		t.Error("step not marked edited")
	}
	// Removing the wasted lower versions keeps the final file as is.
	s = NewState(testArchive())
	_ = s.RemovePath(0, "etc/app.conf")
	_ = s.RemovePath(2, "etc/app.conf")
	if got := finalFS(t, s.Layers())["etc/app.conf"]; got != "v3" {
		t.Errorf("app.conf = %q, want v3", got)
	}
}

func TestPurgeAndPut(t *testing.T) {
	s := NewState(testArchive())
	if err := s.PurgePath("etc/app.conf"); err != nil {
		t.Fatal(err)
	}
	for i, l := range s.Layers() {
		for _, e := range l.Entries {
			if e.Path == "etc/app.conf" {
				t.Errorf("layer %d still has app.conf", i)
			}
		}
	}
	if err := s.PutFile(4, NewFile{Path: "/etc/app.conf", Mode: 0o600, Content: []byte("v4")}); err != nil {
		t.Fatal(err)
	}
	fsys := finalFS(t, s.Layers())
	if fsys["etc/app.conf"] != "v4" {
		t.Errorf("app.conf = %q", fsys["etc/app.conf"])
	}
	if err := s.PutFile(4, NewFile{Path: "etc/app.conf", Content: []byte("v5")}); err != nil {
		t.Fatal(err)
	}
	if got := finalFS(t, s.Layers())["etc/app.conf"]; got != "v5" {
		t.Errorf("second put = %q", got)
	}
}

func TestValidateHardlinks(t *testing.T) {
	a := &Archive{
		History: []History{{}},
		Layers: []Layer{{Entries: []Entry{
			txt("bin/busybox", "bb"),
			{Path: "bin/sh", Hardlink: "bin/busybox"},
		}}},
	}
	s := NewState(a)
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	_ = s.RemovePath(0, "bin/busybox")
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "/bin/sh") {
		t.Errorf("err = %v", err)
	}
}

// saveWithRootfs builds a "docker save" archive whose config has diff_ids.
func saveWithRootfs(t *testing.T, history []map[string]any, layers ...[]byte) []byte {
	t.Helper()
	var ids, names []string
	blobs := map[string][]byte{}
	for i, l := range layers {
		sum := sha256.Sum256(l)
		ids = append(ids, "sha256:"+hex.EncodeToString(sum[:]))
		name := "blobs/sha256/layer" + string(rune('a'+i))
		names = append(names, name)
		blobs[name] = l
	}
	cfg := map[string]any{
		"architecture": "amd64",
		"config":       map[string]any{"Env": []string{"A=1"}},
		"rootfs":       map[string]any{"type": "layers", "diff_ids": ids},
		"history":      history,
	}
	return saveArchive(t, cfg, blobs, names)
}

func TestBuildRoundTrip(t *testing.T) {
	base := layerTar(t,
		file{name: "etc", dir: true},
		file{name: "etc/app.conf", body: "v1"},
		file{name: "bin", dir: true},
		file{name: "bin/busybox", body: "bb"},
		file{name: "bin/sh", hardlink: "bin/busybox"},
	)
	top := layerTar(t, file{name: "etc/app.conf", body: "v2"}, file{name: "etc/.wh.gone"})
	extra := layerTar(t, file{name: "srv", dir: true}, file{name: "srv/x", body: "x"})
	data := saveWithRootfs(t, []map[string]any{
		{"created_by": "base", "author": "me"},
		{"created_by": "top"},
		{"created_by": "ENV A=1", "empty_layer": true},
		{"created_by": "extra"},
	}, base, top, extra)

	st, err := Extract(bytes.NewReader(data), t.TempDir(), DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	s := NewState(st.Archive)
	if err := s.RemovePath(0, "etc/app.conf"); err != nil { // wasted v1
		t.Fatal(err)
	}
	if err := s.PutFile(1, NewFile{Path: "etc/new.conf", Mode: 0o640, UID: 1, GID: 2, Content: []byte("new")}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var stages []Stage
	if err := Build(st, s, "demo:edited", &out, BuildOptions{HistoryComment: "edited with layve"}, func(s Stage) { stages = append(stages, s) }); err != nil {
		t.Fatal(err)
	}
	if len(stages) != 3 {
		t.Errorf("stages = %v", stages)
	}

	// The result is itself a valid archive: read it back.
	got, err := Read(bytes.NewReader(out.Bytes()), DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	fsys := finalFS(t, got.Layers)
	if fsys["etc/app.conf"] != "v2" || fsys["etc/new.conf"] != "new" || fsys["srv/x"] != "x" {
		t.Errorf("fs = %v", fsys)
	}
	for _, e := range got.Layers[0].Entries {
		if e.Path == "etc/app.conf" {
			t.Error("wasted v1 still in base layer")
		}
	}

	// Check config, manifest and that the untouched layer kept its digest.
	tr := tar.NewReader(bytes.NewReader(out.Bytes()))
	files := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		files[hdr.Name], _ = io.ReadAll(tr)
	}
	var manifest []manifestEntry
	_ = json.Unmarshal(files["manifest.json"], &manifest)
	if len(manifest) != 1 || manifest[0].RepoTags[0] != "demo:edited" || len(manifest[0].Layers) != 3 {
		t.Fatalf("manifest = %+v", manifest)
	}
	var cfg struct {
		Arch   string `json:"architecture"`
		Rootfs struct {
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
		History []map[string]any `json:"history"`
	}
	if err := json.Unmarshal(files[manifest[0].Config], &cfg); err != nil {
		t.Fatal(err)
	}
	for i, name := range manifest[0].Layers {
		sum := sha256.Sum256(files[name])
		if cfg.Rootfs.DiffIDs[i] != "sha256:"+hex.EncodeToString(sum[:]) {
			t.Errorf("layer %d diff id mismatch", i)
		}
	}
	if extraSum := sha256.Sum256(extra); cfg.Rootfs.DiffIDs[2] != "sha256:"+hex.EncodeToString(extraSum[:]) {
		t.Error("untouched layer was rewritten")
	}
	if cfg.Arch != "amd64" || cfg.History[0]["author"] != "me" || cfg.History[0]["comment"] != "edited with layve" || cfg.History[3]["comment"] != nil {
		t.Errorf("config not preserved: %+v", cfg)
	}

	// Temporary edited layers live in the extract dir only.
	entries, _ := os.ReadDir(st.dir)
	if len(entries) == 0 {
		t.Error("extract dir empty")
	}
}

func TestBuildRejectsBrokenHardlink(t *testing.T) {
	base := layerTar(t, file{name: "a", body: "1"}, file{name: "b", hardlink: "a"})
	data := saveWithRootfs(t, []map[string]any{{"created_by": "base"}}, base)
	st, err := Extract(bytes.NewReader(data), t.TempDir(), DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	s := NewState(st.Archive)
	_ = s.RemovePath(0, "a")
	if err := Build(st, s, "x:y", io.Discard, BuildOptions{}, nil); err == nil {
		t.Error("expected hard link error")
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "../../evil", Typeflag: tar.TypeReg, Size: 1, Mode: 0o644})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	dir := t.TempDir()
	_, _ = Extract(&buf, dir, DefaultOptions)
	if _, err := os.Stat(dir + "/../evil"); err == nil {
		t.Error("archive escaped the extract dir")
	}
}

func TestBuildWithoutHistoryComment(t *testing.T) {
	base := layerTar(t, file{name: "a", body: "1"}, file{name: "b", body: "2"})
	data := saveWithRootfs(t, []map[string]any{{"created_by": "base", "comment": "original"}}, base)
	st, err := Extract(bytes.NewReader(data), t.TempDir(), DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	s := NewState(st.Archive)
	_ = s.RemovePath(0, "b")
	var out bytes.Buffer
	if err := Build(st, s, "x:y", &out, BuildOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("edited with")) || !bytes.Contains(out.Bytes(), []byte(`"comment":"original"`)) {
		t.Error("history comment changed although no comment was requested")
	}
}
