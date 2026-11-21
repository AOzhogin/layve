package imagearchive

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"
)

// Stored is a "docker save" archive unpacked into a directory.
type Stored struct {
	dir        string
	config     map[string]json.RawMessage
	history    []map[string]any
	layerFiles []string // per original layer, absolute paths
	diffIDs    []string
	// Archive is the metadata, as Read would return it.
	Archive *Archive
}

// Extract unpacks a "docker save" stream into dir and parses it. Layer
// metadata is read from the unpacked files, so the stream is read once.
func Extract(r io.Reader, dir string, opts Options) (*Stored, error) {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		dst, err := safeJoin(dir, hdr.Name)
		if err != nil {
			return nil, err
		}
		if err := writeFile(dst, tr); err != nil {
			return nil, err
		}
	}
	return open(dir, opts)
}

func safeJoin(dir, name string) (string, error) {
	clean := path.Clean("/" + name)
	if clean == "/" {
		return "", fmt.Errorf("bad archive entry %q", name)
	}
	return filepath.Join(dir, filepath.FromSlash(clean)), nil
}

func writeFile(dst string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return f.Close()
}

func open(dir string, opts Options) (*Stored, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, ErrNoImage
	}
	var manifest []manifestEntry
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest.json: %w", err)
	}
	if len(manifest) == 0 {
		return nil, ErrNoImage
	}
	m := manifest[0]
	st := &Stored{dir: dir, Archive: &Archive{}}

	cfgPath, err := safeJoin(dir, m.Config)
	if err != nil {
		return nil, err
	}
	cfgData, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read image config: %w", err)
	}
	if err := json.Unmarshal(cfgData, &st.config); err != nil {
		return nil, fmt.Errorf("parse image config: %w", err)
	}
	var rootfs struct {
		DiffIDs []string `json:"diff_ids"`
	}
	if raw, ok := st.config["rootfs"]; ok {
		_ = json.Unmarshal(raw, &rootfs)
	}
	st.diffIDs = rootfs.DiffIDs
	if raw, ok := st.config["history"]; ok {
		if err := json.Unmarshal(raw, &st.history); err != nil {
			return nil, fmt.Errorf("parse history: %w", err)
		}
	}
	if len(st.diffIDs) != len(m.Layers) {
		return nil, fmt.Errorf("config lists %d layers, manifest %d", len(st.diffIDs), len(m.Layers))
	}

	rd := &reader{opts: opts, budget: opts.MaxTotalSize}
	for _, name := range m.Layers {
		p, err := safeJoin(dir, name)
		if err != nil {
			return nil, err
		}
		l, err := readLayerFile(rd, p)
		if err != nil {
			return nil, fmt.Errorf("read layer %s: %w", name, err)
		}
		st.layerFiles = append(st.layerFiles, p)
		st.Archive.Layers = append(st.Archive.Layers, *l)
	}

	var cfg imageConfig
	_ = json.Unmarshal(cfgData, &cfg)
	for _, h := range cfg.History {
		st.Archive.History = append(st.Archive.History, History{CreatedBy: h.CreatedBy, Created: h.Created, EmptyLayer: h.EmptyLayer})
	}
	if countNonEmpty(st.Archive.History) != len(st.Archive.Layers) {
		st.Archive.History = make([]History, len(st.Archive.Layers))
		st.history = make([]map[string]any, len(st.Archive.Layers))
		for i := range st.history {
			st.history[i] = map[string]any{}
		}
	}
	return st, nil
}

func readLayerFile(rd *reader, p string) (*Layer, error) {
	r, closeFn, err := openLayer(p)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	return rd.readLayer(r)
}

// openLayer opens a layer tarball, transparently decompressing gzip.
func openLayer(p string) (io.Reader, func(), error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, nil, err
	}
	br := bufio.NewReader(f)
	head, _ := br.Peek(2)
	if len(head) == 2 && head[0] == 0x1f && head[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		return gz, func() { _ = gz.Close(); _ = f.Close() }, nil
	}
	return br, func() { _ = f.Close() }, nil
}

// BuildOptions tunes Build.
type BuildOptions struct {
	// HistoryComment is set on the history step of every edited layer;
	// empty leaves comments as they were.
	HistoryComment string
}

// Stage names a phase of Build for progress reporting.
type Stage struct {
	Layer, Layers int // 1-based layer being written
}

// Build writes the edited image as a "docker load" archive tagged tag.
// Untouched layers are copied as is; edited ones are rebuilt in tmp.
func Build(st *Stored, s *State, tag string, w io.Writer, opts BuildOptions, progress func(Stage)) error {
	if err := s.Validate(); err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	var layerNames, diffIDs []string
	for vi, v := range s.vlayers {
		if progress != nil {
			progress(Stage{Layer: vi + 1, Layers: len(s.vlayers)})
		}
		file, diffID := "", ""
		if v.untouched() {
			src := v.Parts[0].Source
			file, diffID = st.layerFiles[src], st.diffIDs[src]
		} else {
			var err error
			file, diffID, err = st.buildLayer(s, vi)
			if err != nil {
				return fmt.Errorf("layer %d: %w", vi+1, err)
			}
		}
		name := fmt.Sprintf("%d/layer.tar", vi)
		if err := copyFileInto(tw, name, file); err != nil {
			return err
		}
		layerNames = append(layerNames, name)
		diffIDs = append(diffIDs, diffID)
	}

	cfg, err := st.editedConfig(s, diffIDs, opts.HistoryComment)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(cfg)
	cfgName := hex.EncodeToString(sum[:]) + ".json"
	if err := writeBytes(tw, cfgName, cfg); err != nil {
		return err
	}
	manifest, err := json.Marshal([]manifestEntry{{Config: cfgName, Layers: layerNames, RepoTags: []string{tag}}})
	if err != nil {
		return err
	}
	if err := writeBytes(tw, "manifest.json", manifest); err != nil {
		return err
	}
	return tw.Close()
}

func (st *Stored) editedConfig(s *State, diffIDs []string, comment string) ([]byte, error) {
	cfg := make(map[string]json.RawMessage, len(st.config))
	for k, v := range st.config {
		cfg[k] = v
	}
	rootfs, err := json.Marshal(map[string]any{"type": "layers", "diff_ids": diffIDs})
	if err != nil {
		return nil, err
	}
	cfg["rootfs"] = rootfs

	var history []map[string]any
	for _, step := range s.Steps() {
		h := map[string]any{}
		for k, v := range st.history[step.Orig] {
			h[k] = v
		}
		if step.EmptyLayer {
			h["empty_layer"] = true
		}
		if step.Edited && comment != "" {
			h["comment"] = comment
		}
		history = append(history, h)
	}
	if cfg["history"], err = json.Marshal(history); err != nil {
		return nil, err
	}
	return json.Marshal(cfg)
}

// buildLayer writes an edited layer to a temporary tarball and returns its
// path and diff ID.
func (st *Stored) buildLayer(s *State, vi int) (string, string, error) {
	f, err := os.CreateTemp(st.dir, "edited-*.tar")
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	h := sha256.New()
	bw := bufio.NewWriterSize(io.MultiWriter(f, h), 1<<20)
	tw := tar.NewWriter(bw)

	v := s.vlayers[vi]
	r := s.resolve(v)
	want := map[ref]bool{}
	for _, it := range r.chosen {
		want[it.ref] = true
	}
	var links []*tar.Header
	for pi, p := range v.Parts {
		var err error
		if p.Source < 0 {
			err = writeAdded(tw, p.Files, pi, want)
		} else {
			links, err = st.copyChosen(tw, p.Source, pi, want, links)
		}
		if err != nil {
			return "", "", err
		}
	}
	for _, hdr := range links {
		if err := tw.WriteHeader(hdr); err != nil {
			return "", "", err
		}
	}
	for _, e := range r.synth {
		if err := tw.WriteHeader(markerHeader(e)); err != nil {
			return "", "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", "", err
	}
	if err := bw.Flush(); err != nil {
		return "", "", err
	}
	return f.Name(), "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// copyChosen streams the chosen entries of an original layer. Indexes
// match Read, which skips the same entries via toEntry. Hard links are
// collected and written after all regular entries.
func (st *Stored) copyChosen(tw *tar.Writer, src, part int, want map[ref]bool, links []*tar.Header) ([]*tar.Header, error) {
	r, closeFn, err := openLayer(st.layerFiles[src])
	if err != nil {
		return nil, err
	}
	defer closeFn()
	tr := tar.NewReader(r)
	idx := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return links, nil
		}
		if err != nil {
			return nil, err
		}
		if _, ok := toEntry(hdr); !ok {
			continue
		}
		i := idx
		idx++
		if !want[ref{part, i}] {
			continue
		}
		if hdr.Typeflag == tar.TypeLink {
			links = append(links, hdr)
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		// tar.Reader stops at hdr.Size, and the data is the image's own
		// uncompressed layer, so this copies no more than the image holds.
		if _, err := io.Copy(tw, tr); err != nil { //nolint:gosec // G110: bounded by the tar entry
			return nil, err
		}
	}
}

func writeAdded(tw *tar.Writer, files []NewFile, part int, want map[ref]bool) error {
	for i, f := range files {
		if !want[ref{part, i}] {
			continue
		}
		data := f.Content
		if data == nil {
			var err error
			if data, err = os.ReadFile(f.HostPath); err != nil {
				return fmt.Errorf("read %s: %w", f.HostPath, err)
			}
		}
		hdr := &tar.Header{
			Name:     f.Path,
			Typeflag: tar.TypeReg,
			Mode:     int64(f.Mode.Perm()),
			Uid:      f.UID,
			Gid:      f.GID,
			Size:     int64(len(data)),
			ModTime:  time.Now().Truncate(time.Second),
			Format:   tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	return nil
}

func markerHeader(e Entry) *tar.Header {
	name := path.Join(path.Dir(e.Path), whiteoutPrefix+path.Base(e.Path))
	if e.Opaque {
		name = path.Join(e.Path, opaqueMarker)
	}
	return &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Format: tar.FormatPAX}
}

func copyFileInto(tw *tar.Writer, name, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: info.Size()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func writeBytes(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
