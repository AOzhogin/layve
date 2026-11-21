package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AOzhogin/layve/internal/pkg/dockerctx"
)

// fakeDaemon answers pings with apiVersion and counts manifest lookups.
func fakeDaemon(t *testing.T, apiVersion string, manifestCalls *atomic.Int32) *Images {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", apiVersion)
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			_, _ = w.Write([]byte("OK"))
		case strings.Contains(r.URL.Path, "/images/") && r.URL.Query().Get("manifests") == "1":
			manifestCalls.Add(1)
			_, _ = w.Write([]byte(`{"Id":"sha256:abc","Manifests":[
				{"Kind":"image","Available":false,"ImageData":{"Platform":{"os":"linux","architecture":"amd64"}}},
				{"Kind":"image","Available":true,"ImageData":{"Platform":{"os":"linux","architecture":"arm64","variant":"v8"}}}]}`))
		case strings.HasSuffix(r.URL.Path, "/version"):
			_, _ = w.Write([]byte(`{"Os":"linux","Arch":"amd64","ApiVersion":"` + apiVersion + `"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	r, err := NewImagesFor(dockerctx.Context{Name: "fake", Host: "tcp://" + strings.TrimPrefix(srv.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestLocalPlatformOldDaemon(t *testing.T) {
	var calls atomic.Int32
	r := fakeDaemon(t, "1.41", &calls)
	p, err := r.localPlatform(context.Background(), "nginx:1.27-alpine")
	if err != nil || p != nil {
		t.Fatalf("platform = %v, err = %v; want nil, nil", p, err)
	}
	if calls.Load() != 0 {
		t.Error("asked an API 1.41 daemon for manifests")
	}
}

func TestLocalPlatformPicksPulledOne(t *testing.T) {
	var calls atomic.Int32
	r := fakeDaemon(t, "1.48", &calls)
	p, err := r.localPlatform(context.Background(), "nginx:1.27-alpine")
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.Architecture != "arm64" || p.Variant != "v8" {
		t.Fatalf("platform = %+v, want the pulled linux/arm64/v8", p)
	}
	if calls.Load() != 1 {
		t.Errorf("manifest lookups = %d", calls.Load())
	}
}
