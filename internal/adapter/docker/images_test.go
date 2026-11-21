package docker

import (
	"io/fs"
	"time"

	"testing"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/pkg/imagearchive"

	apiimage "github.com/moby/moby/api/types/image"
)

func TestToDomain(t *testing.T) {
	img, err := toDomain(apiimage.Summary{
		ID:       "sha256:0123456789abcdef",
		RepoTags: []string{"nginx:1.27", "<none>:<none>"},
		Size:     1000,
		Created:  1_700_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	refs := img.References()
	if len(refs) != 1 || refs[0].String() != "nginx:1.27" {
		t.Errorf("references = %v", refs)
	}
	if img.Size().String() != "1kB" || img.Created().Unix() != 1_700_000_000 {
		t.Errorf("size/created = %s / %v", img.Size(), img.Created())
	}
}

func TestToDomainDangling(t *testing.T) {
	img, err := toDomain(apiimage.Summary{ID: "sha256:abc", RepoTags: []string{"<none>:<none>"}})
	if err != nil {
		t.Fatal(err)
	}
	if !img.Dangling() {
		t.Error("expected dangling image")
	}
}

func TestToDomainRejectsBadData(t *testing.T) {
	if _, err := toDomain(apiimage.Summary{ID: ""}); err == nil {
		t.Error("expected error for empty id")
	}
	if _, err := toDomain(apiimage.Summary{ID: "sha256:abc", Size: -1}); err == nil {
		t.Error("expected error for negative size")
	}
}

func TestMergeDetails(t *testing.T) {
	size, _ := image.NewSize(42)
	history := []image.Layer{
		image.NewLayer("ADD rootfs /", size, time.Time{}, false),
		image.NewLayer("ENV A=1", image.Size{}, time.Time{}, true),
		image.NewLayer("COPY app /app", size, time.Time{}, false),
	}
	a := &imagearchive.Archive{
		History: []imagearchive.History{{CreatedBy: "ADD rootfs /"}, {CreatedBy: "ENV A=1", EmptyLayer: true}, {CreatedBy: "COPY app /app"}},
		Layers: []imagearchive.Layer{
			{Entries: []imagearchive.Entry{{Path: "etc/hosts", Size: 3}}},
			{Entries: []imagearchive.Entry{{Path: "etc/hosts", Size: 5}, {Path: "app", Dir: true}}},
		},
	}
	got, err := details(a, history, image.EditPlan{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !got[1].Layer().Empty() || len(got[1].Changes()) != 0 {
		t.Fatalf("details = %+v", got)
	}
	top := got[2].Changes()
	if len(top) != 2 || top[0].Path() != "app" || top[0].Kind() != image.ChangeAdded || top[1].Kind() != image.ChangeModified {
		t.Errorf("top changes = %+v", top)
	}
	if got[2].Layer().Size().Bytes() != 42 {
		t.Errorf("size from API history lost: %v", got[2].Layer().Size())
	}

	// With a plan: merging both layers leaves one layer on the last step.
	merged, err := details(a, history, image.EditPlan{}.With(image.MergeLayers(0, 2)))
	if err != nil {
		t.Fatal(err)
	}
	if !merged[0].Layer().Empty() || merged[2].Merged() != 2 || !merged[2].Edited() {
		t.Errorf("merged details = %+v", merged)
	}
	if _, err := details(a, history, image.EditPlan{}.With(image.DropLayer(1))); err == nil {
		t.Error("dropping a metadata-only step should fail")
	}
}

func TestToChangesCarriesLinksAndPreview(t *testing.T) {
	out, err := toChanges([]imagearchive.Change{
		{Path: "bin/sh", Kind: imagearchive.Added, Symlink: "busybox", Resolved: "bin/busybox", Mode: fs.ModeSymlink | 0o777},
		{Path: "etc/hosts", Kind: imagearchive.Modified, Preview: imagearchive.PreviewText, Content: []byte("x"), UID: 1, GID: 2},
		{Path: "ln", Kind: imagearchive.Added, Hardlink: "bin/busybox"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if l := out[0].Link(); l.Target() != "busybox" || l.Resolved() != "bin/busybox" || l.Hard() || out[0].Mode()&fs.ModeSymlink == 0 {
		t.Errorf("symlink = %+v mode=%v", l, out[0].Mode())
	}
	if p := out[1].Preview(); p.State() != image.PreviewText || p.Text() != "x" {
		t.Errorf("preview = %+v", p)
	}
	if uid, gid := out[1].Owner(); uid != 1 || gid != 2 {
		t.Errorf("owner = %d:%d", uid, gid)
	}
	if !out[2].Link().Hard() {
		t.Error("hardlink lost")
	}
}
