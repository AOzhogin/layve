package image

import (
	"errors"
	"testing"
	"time"
)

func TestParseReference(t *testing.T) {
	cases := []struct {
		in, repo, tag string
	}{
		{"nginx", "nginx", "latest"},
		{"nginx:1.27-alpine", "nginx", "1.27-alpine"},
		{"ghcr.io/org/app:v2", "ghcr.io/org/app", "v2"},
		{"localhost:5000/app", "localhost:5000/app", "latest"},
		{"localhost:5000/app:dev", "localhost:5000/app", "dev"},
	}
	for _, c := range cases {
		ref, err := ParseReference(c.in)
		if err != nil {
			t.Fatalf("ParseReference(%q): %v", c.in, err)
		}
		if ref.Repository() != c.repo || ref.Tag() != c.tag {
			t.Errorf("ParseReference(%q) = %s / %s, want %s / %s", c.in, ref.Repository(), ref.Tag(), c.repo, c.tag)
		}
	}
}

func TestParseReferenceInvalid(t *testing.T) {
	for _, in := range []string{"", "<none>:<none>", "nginx:", ":tag", "a b", "nginx@sha256:abc"} {
		if _, err := ParseReference(in); !errors.Is(err, ErrInvalidReference) {
			t.Errorf("ParseReference(%q) err = %v, want ErrInvalidReference", in, err)
		}
	}
}

func TestID(t *testing.T) {
	if _, err := NewID("  "); !errors.Is(err, ErrEmptyID) {
		t.Fatalf("NewID(blank) err = %v", err)
	}
	id, err := NewID("sha256:0123456789abcdef0123")
	if err != nil {
		t.Fatal(err)
	}
	if got := id.Short(); got != "0123456789ab" {
		t.Errorf("Short() = %q", got)
	}
}

func TestSize(t *testing.T) {
	if _, err := NewSize(-1); !errors.Is(err, ErrNegativeSize) {
		t.Fatalf("NewSize(-1) err = %v", err)
	}
}

func TestImageReferencesAreCopied(t *testing.T) {
	id, _ := NewID("sha256:abc")
	ref, _ := ParseReference("nginx")
	refs := []Reference{ref}
	img := New(id, refs, Size{}, time.Time{})
	refs[0] = Reference{}
	img.References()[0] = Reference{}
	if img.References()[0] != ref {
		t.Error("Image references were mutated from outside")
	}
	if img.Dangling() {
		t.Error("tagged image reported as dangling")
	}
	if !New(id, nil, Size{}, time.Time{}).Dangling() {
		t.Error("untagged image not reported as dangling")
	}
}
