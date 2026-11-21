// Package imagearchive reads image archives produced by "docker save"
// (both the OCI layout and the legacy Docker layout) in a single streaming
// pass, keeping only file metadata in memory.
package imagearchive

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// maxRawBlob caps how much of a non-layer blob (configs, manifests) is kept.
const maxRawBlob = 8 << 20

const (
	whiteoutPrefix = ".wh."
	opaqueMarker   = ".wh..wh..opq"
)

// Entry is one path inside a layer tarball.
type Entry struct {
	Path string
	Size int64
	Dir  bool
	Mode fs.FileMode
	UID  int
	GID  int
	// Symlink holds the target of a symbolic link, as written in the tar.
	Symlink string
	// Hardlink holds the image path a hard link points to.
	Hardlink string
	// Preview tells whether Content holds the file's text.
	Preview PreviewState
	Content []byte
	// Whiteout marks Path as deleted from lower layers.
	Whiteout bool
	// Opaque marks directory Path as hiding all lower-layer contents.
	Opaque bool
}

// PreviewState says why a file's content is or is not available.
type PreviewState uint8

// Preview states.
const (
	PreviewNone     PreviewState = iota // not a regular file, or empty
	PreviewText                         // Content holds the whole file
	PreviewBinary                       // content is not UTF-8 text
	PreviewTooLarge                     // larger than Options.MaxFileSize
	PreviewSkipped                      // Options.MaxTotalSize already used up
)

// Options controls which file contents are kept for preview.
type Options struct {
	// MaxFileSize is the largest file whose text is kept.
	MaxFileSize int64
	// MaxTotalSize caps the text kept across the whole archive.
	MaxTotalSize int64
}

// DefaultOptions keeps text files up to 64kB, 64MB in total.
var DefaultOptions = Options{MaxFileSize: 64 << 10, MaxTotalSize: 64 << 20}

// Layer is the list of entries of one layer tarball.
type Layer struct {
	Entries []Entry
}

// History is one build step from the image config.
type History struct {
	CreatedBy  string
	Created    time.Time
	EmptyLayer bool
}

// Archive is the parsed content of a single-image archive.
type Archive struct {
	History []History
	// Layers are in application order, bottom first.
	Layers []Layer
}

// ErrNoImage is returned when the archive has no image manifest.
var ErrNoImage = errors.New("archive contains no image manifest")

type manifestEntry struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags,omitempty"`
	Layers   []string `json:"Layers"`
}

type imageConfig struct {
	History []struct {
		Created    time.Time `json:"created"`
		CreatedBy  string    `json:"created_by"`
		EmptyLayer bool      `json:"empty_layer"`
	} `json:"history"`
}

// Read parses the first image of a "docker save" stream.
func Read(r io.Reader, opts Options) (*Archive, error) {
	rd := &reader{opts: opts, budget: opts.MaxTotalSize}
	layers := map[string]Layer{}
	raw := map[string][]byte{}
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
		name := path.Clean(hdr.Name)
		layer, data, err := rd.readBlob(tr, hdr.Size)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if layer != nil {
			layers[name] = *layer
		} else if data != nil {
			raw[name] = data
		}
	}
	return rd.assemble(layers, raw)
}

// readBlob returns a parsed layer if the blob is a (possibly gzipped) tar,
// otherwise the raw bytes if the blob is small enough to be metadata.
type reader struct {
	opts   Options
	budget int64
}

func (rd *reader) readBlob(r io.Reader, size int64) (*Layer, []byte, error) {
	br := bufio.NewReaderSize(r, 4096)
	head, _ := br.Peek(512)
	switch {
	case len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, err
		}
		defer gz.Close()
		l, err := rd.readLayer(gz)
		return l, nil, err
	case len(head) >= 262 && string(head[257:262]) == "ustar":
		l, err := rd.readLayer(br)
		return l, nil, err
	case size <= maxRawBlob:
		data, err := io.ReadAll(br)
		return nil, data, err
	default:
		return nil, nil, nil
	}
}

func (rd *reader) readLayer(r io.Reader) (*Layer, error) {
	tr := tar.NewReader(r)
	var l Layer
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return &l, nil
		}
		if err != nil {
			return nil, err
		}
		e, ok := toEntry(hdr)
		if !ok {
			continue
		}
		if hdr.Typeflag == tar.TypeReg && !e.Whiteout && !e.Opaque {
			if err := rd.capture(&e, tr); err != nil {
				return nil, err
			}
		}
		l.Entries = append(l.Entries, e)
	}
}

// capture keeps the content of small text files within the budget.
func (rd *reader) capture(e *Entry, r io.Reader) error {
	switch {
	case e.Size == 0:
		return nil
	case e.Size > rd.opts.MaxFileSize:
		e.Preview = PreviewTooLarge
		return nil
	case e.Size > rd.budget:
		e.Preview = PreviewSkipped
		return nil
	}
	data := make([]byte, e.Size)
	if _, err := io.ReadFull(r, data); err != nil {
		return fmt.Errorf("read %s: %w", e.Path, err)
	}
	if !isText(data) {
		e.Preview = PreviewBinary
		return nil
	}
	e.Preview, e.Content = PreviewText, data
	rd.budget -= e.Size
	return nil
}

func isText(b []byte) bool {
	return !bytes.ContainsRune(b, 0) && utf8.Valid(b)
}

func toEntry(hdr *tar.Header) (Entry, bool) {
	p := strings.TrimPrefix(path.Clean("/"+hdr.Name), "/")
	if p == "" {
		return Entry{}, false
	}
	dir, base := path.Split(p)
	dir = strings.TrimSuffix(dir, "/")
	switch {
	case base == opaqueMarker:
		if dir == "" {
			return Entry{}, false
		}
		return Entry{Path: dir, Dir: true, Opaque: true}, true
	case strings.HasPrefix(base, whiteoutPrefix):
		return Entry{Path: path.Join(dir, strings.TrimPrefix(base, whiteoutPrefix)), Whiteout: true}, true
	}
	e := Entry{
		Path: p,
		Size: hdr.Size,
		Dir:  hdr.Typeflag == tar.TypeDir,
		Mode: hdr.FileInfo().Mode(),
		UID:  hdr.Uid,
		GID:  hdr.Gid,
	}
	switch hdr.Typeflag {
	case tar.TypeSymlink:
		e.Symlink = hdr.Linkname
	case tar.TypeLink:
		e.Hardlink = strings.TrimPrefix(path.Clean("/"+hdr.Linkname), "/")
	}
	return e, true
}

func (rd *reader) assemble(layers map[string]Layer, raw map[string][]byte) (*Archive, error) {
	data, ok := raw["manifest.json"]
	if !ok {
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

	a := &Archive{}
	for _, name := range m.Layers {
		name = path.Clean(name)
		l, ok := layers[name]
		if !ok {
			// Tiny or empty layers may lack the ustar magic; parse them now.
			data, found := raw[name]
			if !found {
				return nil, fmt.Errorf("layer %s missing from archive", name)
			}
			parsed, err := rd.readLayer(bytes.NewReader(data))
			if err != nil {
				return nil, fmt.Errorf("parse layer %s: %w", name, err)
			}
			l = *parsed
		}
		a.Layers = append(a.Layers, l)
	}

	var cfg imageConfig
	if data, ok := raw[path.Clean(m.Config)]; ok {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse image config: %w", err)
		}
	}
	for _, h := range cfg.History {
		a.History = append(a.History, History{CreatedBy: h.CreatedBy, Created: h.Created, EmptyLayer: h.EmptyLayer})
	}
	if countNonEmpty(a.History) != len(a.Layers) {
		// Missing or inconsistent history: describe each layer on its own.
		a.History = make([]History, len(a.Layers))
	}
	return a, nil
}

func countNonEmpty(h []History) int {
	n := 0
	for _, e := range h {
		if !e.EmptyLayer {
			n++
		}
	}
	return n
}
