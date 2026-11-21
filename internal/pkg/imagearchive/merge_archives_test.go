package imagearchive

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

func archive(t *testing.T, files map[string]string, order []string) func() (io.ReadCloser, error) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "blobs/", Typeflag: tar.TypeDir, Mode: 0o755})
	for _, name := range order {
		data := files[name]
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))})
		_, _ = tw.Write([]byte(data))
	}
	_ = tw.Close()
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf.Bytes())), nil }
}

func TestMerge(t *testing.T) {
	a := archive(t, map[string]string{
		"blobs/sha256/shared": "base layer",
		"blobs/sha256/a":      "config a",
		"oci-layout":          `{"imageLayoutVersion":"1.0.0"}`,
		"index.json":          `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"sha256:a"}]}`,
		"manifest.json":       `[{"Config":"blobs/sha256/a","RepoTags":["a:1"],"Layers":["blobs/sha256/shared"]}]`,
		"repositories":        `{"a":{"1":"aaa"}}`,
	}, []string{"blobs/sha256/shared", "blobs/sha256/a", "oci-layout", "index.json", "manifest.json", "repositories"})
	b := archive(t, map[string]string{
		"blobs/sha256/shared": "base layer",
		"blobs/sha256/b":      "config b",
		"index.json":          `{"schemaVersion":2,"manifests":[{"digest":"sha256:b"}]}`,
		"manifest.json":       `[{"Config":"blobs/sha256/b","RepoTags":["b:2"],"Layers":["blobs/sha256/shared"]}]`,
	}, []string{"blobs/sha256/shared", "blobs/sha256/b", "index.json", "manifest.json"})

	var out bytes.Buffer
	if err := Merge(&out, []func() (io.ReadCloser, error){a, b}); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	count := map[string]int{}
	tr := tar.NewReader(&out)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		files[hdr.Name] = string(data)
		count[hdr.Name]++
	}
	if count["blobs/sha256/shared"] != 1 || files["blobs/sha256/b"] != "config b" || count["blobs/"] != 1 {
		t.Errorf("blobs: %v", count)
	}
	var manifest []manifestEntry
	_ = json.Unmarshal([]byte(files["manifest.json"]), &manifest)
	if len(manifest) != 2 || manifest[1].RepoTags[0] != "b:2" {
		t.Errorf("manifest.json = %s", files["manifest.json"])
	}
	var idx struct {
		SchemaVersion int               `json:"schemaVersion"`
		Manifests     []json.RawMessage `json:"manifests"`
	}
	_ = json.Unmarshal([]byte(files["index.json"]), &idx)
	if idx.SchemaVersion != 2 || len(idx.Manifests) != 2 {
		t.Errorf("index.json = %s", files["index.json"])
	}
	if files["oci-layout"] == "" || files["repositories"] != `{"a":{"1":"aaa"}}` {
		t.Errorf("oci-layout %q, repositories %q", files["oci-layout"], files["repositories"])
	}
}
