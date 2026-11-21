package imagearchive

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
)

// Merge writes one "docker save" archive that combines several, so images
// saved separately (for example each as its own platform) load in one go.
// Blobs are content-addressed, so a path seen twice is written once;
// manifest.json, index.json and repositories are merged; oci-layout is
// kept once.
func Merge(w io.Writer, archives []func() (io.ReadCloser, error)) error {
	tw := tar.NewWriter(w)
	seen := map[string]bool{}
	var manifest []json.RawMessage
	var index struct {
		SchemaVersion int               `json:"schemaVersion"`
		MediaType     string            `json:"mediaType,omitempty"`
		Manifests     []json.RawMessage `json:"manifests"`
	}
	repositories := map[string]map[string]string{}
	var layout []byte

	for i, open := range archives {
		rc, err := open()
		if err != nil {
			return err
		}
		err = mergeOne(tw, rc, seen, &manifest, &index.Manifests, &index.SchemaVersion, &index.MediaType, repositories, &layout)
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("archive %d: %w", i+1, err)
		}
	}

	write := func(name string, v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return writeBytes(tw, name, data)
	}
	if layout != nil {
		if err := writeBytes(tw, "oci-layout", layout); err != nil {
			return err
		}
	}
	if len(index.Manifests) > 0 {
		if err := write("index.json", index); err != nil {
			return err
		}
	}
	if len(repositories) > 0 {
		if err := write("repositories", repositories); err != nil {
			return err
		}
	}
	if err := write("manifest.json", manifest); err != nil {
		return err
	}
	return tw.Close()
}

func mergeOne(tw *tar.Writer, r io.Reader, seen map[string]bool, manifest, indexManifests *[]json.RawMessage,
	schema *int, mediaType *string, repositories map[string]map[string]string, layout *[]byte) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(hdr.Name)
		switch name {
		case "manifest.json":
			var m []json.RawMessage
			if err := json.NewDecoder(tr).Decode(&m); err != nil {
				return fmt.Errorf("manifest.json: %w", err)
			}
			*manifest = append(*manifest, m...)
			continue
		case "index.json":
			var idx struct {
				SchemaVersion int               `json:"schemaVersion"`
				MediaType     string            `json:"mediaType"`
				Manifests     []json.RawMessage `json:"manifests"`
			}
			if err := json.NewDecoder(tr).Decode(&idx); err != nil {
				return fmt.Errorf("index.json: %w", err)
			}
			*schema, *mediaType = idx.SchemaVersion, idx.MediaType
			*indexManifests = append(*indexManifests, idx.Manifests...)
			continue
		case "repositories":
			var repos map[string]map[string]string
			if err := json.NewDecoder(tr).Decode(&repos); err != nil {
				return fmt.Errorf("repositories: %w", err)
			}
			for repo, tags := range repos {
				if repositories[repo] == nil {
					repositories[repo] = map[string]string{}
				}
				for tag, id := range tags {
					repositories[repo][tag] = id
				}
			}
			continue
		case "oci-layout":
			data, err := io.ReadAll(io.LimitReader(tr, 1<<16))
			if err != nil {
				return err
			}
			*layout = data
			continue
		}
		if seen[name] {
			continue // the same content-addressed blob from another image
		}
		seen[name] = true
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.Copy(tw, tr); err != nil { //nolint:gosec // G110: bounded by the tar entry
				return err
			}
		}
	}
}
