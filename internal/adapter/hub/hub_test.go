package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AOzhogin/layve/internal/pkg/dockerhub"
)

func TestTagsSkipAttestations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"name":"1.0","last_updated":"2026-01-02T00:00:00Z","images":[
			{"os":"linux","architecture":"arm64","variant":"v8","size":2},
			{"os":"unknown","architecture":"unknown","size":1},
			{"os":"linux","architecture":"amd64","size":3},
			{"os":"linux","architecture":"amd64","size":3}]}, {"name":"","images":[]}]}`))
	}))
	defer srv.Close()
	tags, err := New(dockerhub.New(srv.URL, nil)).Tags(context.Background(), "acme/app", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 {
		t.Fatalf("tags = %+v", tags)
	}
	v := tags[0].Variants()
	if len(v) != 2 || v[0].Platform.String() != "linux/amd64" || v[1].Platform.String() != "linux/arm64/v8" {
		t.Errorf("variants = %+v", v)
	}
}
