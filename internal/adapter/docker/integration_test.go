package docker

import (
	"path/filepath"

	"github.com/AOzhogin/layve/internal/domain/registry"

	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/AOzhogin/layve/internal/pkg/dockerctx"

	"github.com/moby/moby/client"

	"github.com/AOzhogin/layve/internal/domain/image"
)

// TestIntegrationWrite edits a real image and runs the result. It needs a
// Docker daemon with nginx:1.27-alpine present:
//
//	LAYVE_INTEGRATION=1 go test -run Integration ./internal/adapter/docker/
func TestIntegrationWrite(t *testing.T) {
	if os.Getenv("LAYVE_INTEGRATION") == "" {
		t.Skip("set LAYVE_INTEGRATION=1 to run against the local daemon")
	}
	const (
		source = "nginx:1.27-alpine"
		target = "layve-it:edited"
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	r, err := NewImages()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	t.Cleanup(func() {
		_, _ = r.cli.ImageRemove(context.Background(), target, client.ImageRemoveOptions{PruneChildren: true})
	})

	id, _ := image.NewID(source)
	in, err := r.Inspect(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := in.Details(image.EditPlan{})
	if err != nil {
		t.Fatal(err)
	}
	first, last, layers := -1, -1, 0
	for i, d := range before {
		if d.Layer().Empty() {
			continue
		}
		layers++
		if first < 0 {
			first = i
		}
		last = i
	}
	if layers < 2 {
		t.Fatalf("%s has %d layers, need at least 2", source, layers)
	}

	purge, _ := image.PurgePath("etc/nginx/conf.d/default.conf")
	put, _ := image.PutFile(last, "etc/hello.txt", []byte("hello from layve\n"), 0o644, 0, 0)
	plan := image.EditPlan{}.With(image.MergeLayers(first, last)).With(purge).With(put)

	preview, err := in.Details(plan)
	if err != nil {
		t.Fatal(err)
	}
	if n := countLayers(preview); n != 1 {
		t.Fatalf("preview has %d layers, want 1", n)
	}

	var stages []string
	ref, _ := image.ParseReference(target)
	start := time.Now()
	if err := r.Write(ctx, id, plan, ref, image.WriteOptions{MarkHistory: true}, func(s string) { stages = append(stages, s) }); err != nil {
		t.Fatal(err)
	}
	t.Logf("written in %v, stages %v", time.Since(start), stages)

	insp, err := r.cli.ImageInspect(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(insp.RootFS.Layers); n != 1 {
		t.Errorf("written image has %d layers, want 1", n)
	}

	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--entrypoint", "sh", target, "-c",
		"cat /etc/hello.txt; test -e /etc/nginx/conf.d/default.conf || echo purged; nginx -v 2>&1").CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, want := range []string{"hello from layve", "purged", "nginx version"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("container output missing %q:\n%s", want, out)
		}
	}
}

func countLayers(d []image.LayerDetail) int {
	n := 0
	for _, x := range d {
		if !x.Layer().Empty() {
			n++
		}
	}
	return n
}

// TestIntegrationKeepsUntouchedLayers checks that editing one layer leaves
// the digests of the others as they were, so registries can reuse them.
func TestIntegrationKeepsUntouchedLayers(t *testing.T) {
	if os.Getenv("LAYVE_INTEGRATION") == "" {
		t.Skip("set LAYVE_INTEGRATION=1 to run against the local daemon")
	}
	const (
		source = "nginx:1.27-alpine"
		target = "layve-it:one-layer"
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	r, err := NewImages()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	t.Cleanup(func() {
		_, _ = r.cli.ImageRemove(context.Background(), target, client.ImageRemoveOptions{PruneChildren: true})
	})

	id, _ := image.NewID(source)
	in, err := r.Inspect(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	steps, _ := in.Details(image.EditPlan{})
	first := -1
	for i, d := range steps {
		if !d.Layer().Empty() {
			first = i
			break
		}
	}
	rm, _ := image.RemoveFromLayer(first, "etc/motd")
	ref, _ := image.ParseReference(target)
	if err := r.Write(ctx, id, image.EditPlan{}.With(rm), ref, image.WriteOptions{}, nil); err != nil {
		t.Fatal(err)
	}

	orig, err := r.cli.ImageInspect(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := r.cli.ImageInspect(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	o, e := orig.RootFS.Layers, edited.RootFS.Layers
	if len(o) != len(e) {
		t.Fatalf("layer count %d -> %d", len(o), len(e))
	}
	if o[0] == e[0] {
		t.Error("edited layer kept its digest")
	}
	for i := 1; i < len(o); i++ {
		if o[i] != e[i] {
			t.Errorf("untouched layer %d changed digest", i)
		}
	}
}

// TestIntegrationSSHTunnel talks to the local daemon through the ssh code
// path: a stand-in "ssh" drops the ssh arguments and runs the same
// "docker system dial-stdio" a remote host would.
func TestIntegrationSSHTunnel(t *testing.T) {
	if os.Getenv("LAYVE_INTEGRATION") == "" {
		t.Skip("set LAYVE_INTEGRATION=1 to run against the local daemon")
	}
	script := filepath.Join(t.TempDir(), "ssh")
	body := "#!/bin/sh\nwhile [ \"$1\" != \"--\" ]; do shift; done\nshift 2\nexec \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAYVE_SSH_COMMAND", script)

	r, err := NewImagesFor(dockerctx.Context{Name: "tunnel", Host: "ssh://user@remote:2222"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	images, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) == 0 {
		t.Fatal("no images through the tunnel")
	}

	// Export and import through the tunnel: a round trip of a small image.
	ref, _ := image.ParseReference("nginx:1.27-alpine")
	rc, err := r.Export(ctx, []image.Target{image.ReferenceTarget(ref)})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if err := r.Import(ctx, rc); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationFind(t *testing.T) {
	if os.Getenv("LAYVE_INTEGRATION") == "" {
		t.Skip("set LAYVE_INTEGRATION=1 to run against the local daemon")
	}
	r, err := NewImages()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	img, err := r.Find(context.Background(), "nginx:1.27-alpine")
	if err != nil {
		t.Fatal(err)
	}
	if img.Dangling() || img.Size().Bytes() == 0 || img.Created().IsZero() {
		t.Errorf("image = %+v", img)
	}
	if _, err := r.Find(context.Background(), "layve-does-not-exist:never"); err == nil {
		t.Error("expected error for a missing image")
	}
}

// TestIntegrationPullPlatform pulls a tiny image for a foreign architecture,
// the way the Docker Hub screen does.
func TestIntegrationPullPlatform(t *testing.T) {
	if os.Getenv("LAYVE_INTEGRATION") == "" {
		t.Skip("set LAYVE_INTEGRATION=1 to run against the local daemon")
	}
	r, err := NewImages()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	native, err := r.Platform(ctx)
	if err != nil || native.OS() != "linux" {
		t.Fatalf("platform = %v, %v", native, err)
	}
	const name = "hello-world:latest"
	t.Cleanup(func() {
		_, _ = r.cli.ImageRemove(context.Background(), name, client.ImageRemoveOptions{Force: true, PruneChildren: true})
	})
	ref, _ := image.ParseReference(name)
	arm, _ := registry.ParsePlatform("linux/arm64/v8")
	var last [2]int64
	if err := r.Pull(ctx, ref, arm, func(done, total int64) { last = [2]int64{done, total} }); err != nil {
		t.Fatal(err)
	}
	insp, err := r.cli.ImageInspect(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if insp.Architecture != "arm64" {
		t.Errorf("pulled %s, want arm64", insp.Architecture)
	}
	if last[1] > 0 && last[0] != last[1] {
		t.Errorf("progress ended at %d of %d", last[0], last[1])
	}
	missing, _ := image.ParseReference("layve-does-not-exist/nope:1")
	if err := r.Pull(ctx, missing, native, nil); err == nil {
		t.Error("expected an error for a missing image")
	}
}
