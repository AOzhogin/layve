package dockerhub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestSearchAndTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/search/repositories/" && r.URL.Query().Get("query") == "nginx":
			w.Write([]byte(`{"results":[{"repo_name":"nginx","short_description":"Official build of Nginx.","star_count":21425,"pull_count":13616996008,"is_official":true}]}`))
		case r.URL.Path == "/v2/repositories/library/nginx/tags":
			if r.URL.Query().Get("name") != "1.27" || r.URL.Query().Get("ordering") != "last_updated" {
				t.Errorf("query = %v", r.URL.Query())
			}
			w.Write([]byte(`{"results":[{"name":"1.27-alpine","last_updated":"2026-11-20T01:52:03.186066Z","images":[
				{"os":"linux","architecture":"amd64","size":26055355},
				{"os":"linux","architecture":"arm64","variant":"v8","size":25000000}]}]}`))
		case r.URL.Path == "/v2/repositories/library/missing/tags":
			http.NotFound(w, r)
		case r.URL.Path == "/v2/repositories/busy/repo/tags":
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			t.Errorf("unexpected %s", r.URL)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, nil)
	ctx := context.Background()

	repos, err := c.Search(ctx, "nginx", 25)
	if err != nil || len(repos) != 1 || !repos[0].Official || repos[0].Pulls != 13616996008 {
		t.Fatalf("Search = %+v, %v", repos, err)
	}
	tags, err := c.Tags(ctx, "nginx", "1.27", 100)
	if err != nil || len(tags) != 1 || len(tags[0].Images) != 2 || tags[0].Images[1].Variant != "v8" || tags[0].LastUpdated.Year() != 2026 {
		t.Fatalf("Tags = %+v, %v", tags, err)
	}
	if _, err := c.Tags(ctx, "missing", "", 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing repo err = %v", err)
	}
	if _, err := c.Tags(ctx, "busy/repo", "", 10); err == nil || err.Error() != "docker hub: rate limit reached, try again in a minute" {
		t.Errorf("rate limit err = %v", err)
	}
}

func TestNamespaced(t *testing.T) {
	cases := map[string]string{"nginx": "library/nginx", "bitnami/redis": "bitnami/redis", "docker.io/library/nginx": "library/nginx", "docker.io/grafana/grafana": "grafana/grafana"}
	for in, want := range cases {
		if got := Namespaced(in); got != want {
			t.Errorf("Namespaced(%q) = %q", in, got)
		}
	}
}

// TestLiveDockerHub hits the real API; set LAYVE_INTEGRATION=1.
func TestLiveDockerHub(t *testing.T) {
	if os.Getenv("LAYVE_INTEGRATION") == "" {
		t.Skip("set LAYVE_INTEGRATION=1 to query hub.docker.com")
	}
	c := New("", nil)
	repos, err := c.Search(context.Background(), "nginx", 5)
	if err != nil || len(repos) == 0 || repos[0].Name != "nginx" {
		t.Fatalf("Search = %+v, %v", repos, err)
	}
	tags, err := c.Tags(context.Background(), "nginx", "alpine", 5)
	if err != nil || len(tags) == 0 || len(tags[0].Images) == 0 {
		t.Fatalf("Tags = %+v, %v", tags, err)
	}
}
