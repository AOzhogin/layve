package image

import (
	"errors"
	"testing"
	"time"
)

func TestNormalizeCommand(t *testing.T) {
	cases := map[string]string{
		"/bin/sh -c #(nop)  CMD [\"nginx\"]":        `CMD ["nginx"]`,
		"/bin/sh -c apt-get update &&   apt-get -y": "RUN apt-get update && apt-get -y",
		"RUN /bin/sh -c go build ./... # buildkit":  "RUN /bin/sh -c go build ./...",
		"COPY app /app # buildkit":                  "COPY app /app",
		"ENV PATH=/usr/local/bin":                   "ENV PATH=/usr/local/bin",
	}
	for in, want := range cases {
		if got := NewLayer(in, Size{}, time.Time{}, false).CreatedBy(); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewFileChange(t *testing.T) {
	c, err := NewFileChange("/etc/./ssl/../hosts", ChangeModified, Size{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Path() != "etc/hosts" || c.Kind().Symbol() != "~" {
		t.Errorf("got %q %s", c.Path(), c.Kind())
	}
	if _, err := NewFileChange("/", ChangeAdded, Size{}, true); !errors.Is(err, ErrInvalidChange) {
		t.Errorf("root path err = %v", err)
	}
	if _, err := NewFileChange("a", 0, Size{}, false); !errors.Is(err, ErrInvalidChange) {
		t.Errorf("zero kind err = %v", err)
	}
}
