package docker

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/pkg/dockerctx"
)

// TestIntegrationRemote runs a full round trip against a real ssh:// context:
// copy an image there, edit it there, copy the result back. Start the test
// host with test/remote/up.sh, then:
//
//	eval "$(test/remote/up.sh /tmp/layve-remote)"
//	LAYVE_INTEGRATION=1 LAYVE_REMOTE_CONTEXT=debian go test -run Remote ./internal/adapter/docker/
func TestIntegrationRemote(t *testing.T) {
	name := os.Getenv("LAYVE_REMOTE_CONTEXT")
	if os.Getenv("LAYVE_INTEGRATION") == "" || name == "" {
		t.Skip("set LAYVE_INTEGRATION=1 and LAYVE_REMOTE_CONTEXT to a running ssh context")
	}
	const (
		source = "nginx:1.27-alpine"
		edited = "layve-it:remote"
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	store := dockerctx.NewStore(dockerctx.ConfigDir(nil), nil)
	rc, err := store.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := NewImagesFor(rc)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	local, err := NewImages()
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	t.Cleanup(func() {
		for _, r := range []*Images{remote, local} {
			for _, ref := range []string{edited, source} {
				if r == local && ref == source {
					continue // keep the local base image
				}
				_, _ = r.cli.ImageRemove(context.Background(), ref, client.ImageRemoveOptions{PruneChildren: true})
			}
		}
	})

	// Local -> remote.
	srcRef, _ := image.ParseReference(source)
	copyImage(ctx, t, local, remote, image.ReferenceTarget(srcRef))

	// Edit on the remote machine.
	id, _ := image.NewID(source)
	in, err := remote.Inspect(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := in.Details(image.EditPlan{})
	if err != nil {
		t.Fatal(err)
	}
	last := len(steps) - 1
	for steps[last].Layer().Empty() {
		last--
	}
	put, _ := image.PutFile(last, "etc/remote.txt", []byte("edited remotely\n"), 0o644, 0, 0)
	editedRef, _ := image.ParseReference(edited)
	if err := remote.Write(ctx, id, image.EditPlan{}.With(put), editedRef, image.WriteOptions{MarkHistory: true}, nil); err != nil {
		t.Fatal(err)
	}
	list, err := remote.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasTag(list, edited) {
		t.Fatalf("%s not on the remote machine", edited)
	}

	// Remote -> local, then run it here.
	copyImage(ctx, t, remote, local, image.ReferenceTarget(editedRef))
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--entrypoint", "cat", edited, "/etc/remote.txt").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "edited remotely") {
		t.Fatalf("run copied image: %v\n%s", err, out)
	}
}

func copyImage(ctx context.Context, t *testing.T, from, to *Images, target image.Target) {
	t.Helper()
	rc, err := from.Export(ctx, []image.Target{target})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if err := to.Import(ctx, io.NopCloser(rc)); err != nil {
		t.Fatal(err)
	}
}

func hasTag(images []image.Image, tag string) bool {
	for _, img := range images {
		for _, r := range img.References() {
			if r.String() == tag {
				return true
			}
		}
	}
	return false
}
