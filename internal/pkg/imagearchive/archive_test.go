package imagearchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type file struct {
	name     string
	body     string
	dir      bool
	symlink  string
	hardlink string
}

func layerTar(t *testing.T, files ...file) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		hdr := &tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		switch {
		case f.dir:
			hdr = &tar.Header{Name: f.name + "/", Mode: 0o755, Typeflag: tar.TypeDir}
		case f.symlink != "":
			hdr = &tar.Header{Name: f.name, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: f.symlink}
		case f.hardlink != "":
			hdr = &tar.Header{Name: f.name, Mode: 0o755, Typeflag: tar.TypeLink, Linkname: f.hardlink}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

// saveArchive mimics "docker save": blobs first, manifest.json last.
func saveArchive(t *testing.T, config any, layers map[string][]byte, order []string) []byte {
	t.Helper()
	cfg, _ := json.Marshal(config)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	write := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(data)
	}
	_ = tw.WriteHeader(&tar.Header{Name: "blobs/", Typeflag: tar.TypeDir, Mode: 0o755})
	for _, name := range order {
		write(name, layers[name])
	}
	write("blobs/sha256/config", cfg)
	manifest, _ := json.Marshal([]manifestEntry{{Config: "blobs/sha256/config", Layers: order}})
	write("manifest.json", manifest)
	_ = tw.Close()
	return buf.Bytes()
}

func TestReadAndDiff(t *testing.T) {
	base := layerTar(t,
		file{name: "etc", dir: true},
		file{name: "etc/hosts", body: "127.0.0.1"},
		file{name: "etc/old.conf", body: "x"},
		file{name: "var", dir: true},
		file{name: "var/cache", dir: true},
		file{name: "var/cache/a", body: "a"},
		file{name: "var/cache/sub", dir: true},
		file{name: "var/cache/sub/b", body: "b"},
		file{name: "tmp", dir: true},
		file{name: "tmp/build.log", body: "log"},
	)
	top := layerTar(t,
		file{name: "etc", dir: true},
		file{name: "etc/hosts", body: "127.0.0.1 app"},
		file{name: "etc/.wh.old.conf"},
		file{name: "app", dir: true},
		file{name: "app/server", body: "bin"},
		file{name: "var/cache/.wh..wh..opq"},
		file{name: "var/cache/new", body: "n"},
		file{name: ".wh.tmp"},
	)
	config := map[string]any{"history": []map[string]any{
		{"created_by": "ADD rootfs /"},
		{"created_by": "ENV A=1", "empty_layer": true},
		{"created_by": "COPY app /app"},
		{"created_by": "CMD [\"/app/server\"]", "empty_layer": true},
	}}
	data := saveArchive(t, config,
		map[string][]byte{"blobs/sha256/base": base, "blobs/sha256/top": gz(t, top)},
		[]string{"blobs/sha256/base", "blobs/sha256/top"})

	a, err := Read(bytes.NewReader(data), DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Layers) != 2 || len(a.History) != 4 || !a.History[1].EmptyLayer {
		t.Fatalf("layers=%d history=%+v", len(a.Layers), a.History)
	}

	diff := Diff(a.Layers)
	got := map[string]ChangeKind{}
	for _, c := range diff[1] {
		got[c.Path] = c.Kind
	}
	want := map[string]ChangeKind{
		"etc/hosts":     Modified,
		"etc/old.conf":  Deleted,
		"app":           Added,
		"app/server":    Added,
		"var/cache/a":   Deleted,
		"var/cache/sub": Deleted,
		"var/cache/new": Added,
		"tmp":           Deleted,
	}
	if len(got) != len(want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
	for p, k := range want {
		if got[p] != k {
			t.Errorf("%s: kind %d, want %d", p, got[p], k)
		}
	}
	if len(diff[0]) != 10 {
		t.Errorf("base layer changes = %d, want 10", len(diff[0]))
	}
}

func TestReadInconsistentHistory(t *testing.T) {
	data := saveArchive(t, map[string]any{}, map[string][]byte{"l": layerTar(t, file{name: "a", body: "1"})}, []string{"l"})
	a, err := Read(bytes.NewReader(data), DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.History) != 1 || a.History[0].EmptyLayer {
		t.Errorf("history = %+v, want one synthesized entry", a.History)
	}
}

func TestReadEmptyLayer(t *testing.T) {
	empty := make([]byte, 1024) // a tar with no entries is just zero blocks
	data := saveArchive(t, map[string]any{}, map[string][]byte{"e": empty}, []string{"e"})
	a, err := Read(bytes.NewReader(data), DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Layers) != 1 || len(a.Layers[0].Entries) != 0 {
		t.Errorf("layers = %+v", a.Layers)
	}
}

func TestReadNoManifest(t *testing.T) {
	var buf bytes.Buffer
	_ = tar.NewWriter(&buf).Close()
	if _, err := Read(&buf, DefaultOptions); !errors.Is(err, ErrNoImage) {
		t.Errorf("err = %v, want ErrNoImage", err)
	}
}

func changesByPath(t *testing.T, layers [][]Change, i int) map[string]Change {
	t.Helper()
	out := map[string]Change{}
	for _, c := range layers[i] {
		out[c.Path] = c
	}
	return out
}

func TestSymlinks(t *testing.T) {
	base := Layer{Entries: []Entry{
		{Path: "usr", Dir: true},
		{Path: "usr/bin", Dir: true},
		{Path: "usr/bin/busybox", Size: 10},
		{Path: "bin", Symlink: "usr/bin"},
	}}
	top := Layer{Entries: []Entry{
		{Path: "usr/bin/sh", Symlink: "busybox"},
		{Path: "usr/local", Dir: true},
		{Path: "usr/local/sh", Symlink: "/bin/sh"},
		{Path: "broken", Symlink: "../nowhere"},
		{Path: "loop-a", Symlink: "loop-b"},
		{Path: "loop-b", Symlink: "loop-a"},
		{Path: "ln", Hardlink: "usr/bin/busybox"},
	}}
	got := changesByPath(t, Diff([]Layer{base, top}), 1)
	cases := []struct {
		path, resolved string
		broken         bool
	}{
		{"usr/bin/sh", "usr/bin/busybox", false},
		{"usr/local/sh", "usr/bin/busybox", false}, // via /bin -> usr/bin
		{"broken", "nowhere", true},
		{"loop-a", "", true},
	}
	for _, c := range cases {
		ch := got[c.path]
		if ch.Broken != c.broken || (c.resolved != "" && ch.Resolved != c.resolved) {
			t.Errorf("%s: resolved=%q broken=%v, want %q %v", c.path, ch.Resolved, ch.Broken, c.resolved, c.broken)
		}
	}
	if got["ln"].Hardlink != "usr/bin/busybox" {
		t.Errorf("hardlink = %q", got["ln"].Hardlink)
	}
}

func TestPreviewCapture(t *testing.T) {
	layer := layerTar(t,
		file{name: "etc/hosts", body: "127.0.0.1 localhost\n"},
		file{name: "bin/app", body: "\x7fELF\x00\x01"},
		file{name: "big.log", body: strings.Repeat("x", 100)},
		file{name: "a.txt", body: strings.Repeat("a", 30)},
		file{name: "b.txt", body: strings.Repeat("b", 30)},
		file{name: "sh", symlink: "bin/app"},
		file{name: "hl", hardlink: "etc/hosts"},
	)
	data := saveArchive(t, map[string]any{}, map[string][]byte{"l": layer}, []string{"l"})
	// hosts (20) + a.txt (30) fit into 60; b.txt does not.
	a, err := Read(bytes.NewReader(data), Options{MaxFileSize: 50, MaxTotalSize: 60})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
	for _, e := range a.Layers[0].Entries {
		got[e.Path] = e
	}
	want := map[string]PreviewState{
		"etc/hosts": PreviewText,
		"bin/app":   PreviewBinary,
		"big.log":   PreviewTooLarge,
		"a.txt":     PreviewText,
		"b.txt":     PreviewSkipped,
		"sh":        PreviewNone,
		"hl":        PreviewNone,
	}
	for p, st := range want {
		if got[p].Preview != st {
			t.Errorf("%s: preview %d, want %d", p, got[p].Preview, st)
		}
	}
	if string(got["etc/hosts"].Content) != "127.0.0.1 localhost\n" {
		t.Errorf("content = %q", got["etc/hosts"].Content)
	}
	if got["sh"].Symlink != "bin/app" || got["hl"].Hardlink != "etc/hosts" {
		t.Errorf("links: %+v %+v", got["sh"], got["hl"])
	}
	if got["etc/hosts"].Mode.Perm() != 0o644 {
		t.Errorf("mode = %v", got["etc/hosts"].Mode)
	}
}
