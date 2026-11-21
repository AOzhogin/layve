package imagearchive

import (
	"io/fs"
	"path"
	"sort"
	"strings"
)

// ChangeKind describes how a layer changed a path.
type ChangeKind uint8

// Change kinds.
const (
	Added ChangeKind = iota + 1
	Modified
	Deleted
)

// maxLinkHops bounds symlink resolution, like the kernel's ELOOP limit.
const maxLinkHops = 40

// Change is a path changed by a layer relative to the layers below it.
// For deletions only Path, Kind and Dir are set.
type Change struct {
	Path string
	Kind ChangeKind
	Size int64
	Dir  bool
	Mode fs.FileMode
	UID  int
	GID  int

	Symlink string
	// Resolved is where Symlink finally leads inside the image, following
	// every intermediate link, as the filesystem looks after this layer.
	Resolved string
	// Broken reports that Resolved does not exist or links loop.
	Broken   bool
	Hardlink string

	Preview PreviewState
	Content []byte
}

// node is one path of the merged filesystem.
type node struct {
	dir  bool
	link string
}

// Diff returns, for every layer, the changes it makes on top of the layers
// below. Directories that already existed are not reported as modified,
// since tarballs re-list parent directories of every changed file.
// When a directory is deleted only the directory itself is reported.
func Diff(layers []Layer) [][]Change {
	fsys := map[string]node{}
	out := make([][]Change, len(layers))
	for i, l := range layers {
		out[i] = diffLayer(l, fsys)
	}
	return out
}

func diffLayer(l Layer, fsys map[string]node) []Change {
	var changes []Change
	deleted := map[string]bool{}

	// Deletions apply to lower layers only, so handle them before additions.
	for _, e := range l.Entries {
		switch {
		case e.Opaque:
			for _, p := range under(fsys, e.Path) {
				deleted[p] = true
			}
		case e.Whiteout:
			if _, ok := fsys[e.Path]; ok {
				deleted[e.Path] = true
				for _, p := range under(fsys, e.Path) {
					deleted[p] = true
				}
			}
		}
	}

	readded := map[string]bool{}
	for _, e := range l.Entries {
		if e.Whiteout || e.Opaque {
			continue
		}
		readded[e.Path] = true
	}
	for p := range deleted {
		if readded[p] {
			continue
		}
		wasDir := fsys[p].dir
		delete(fsys, p)
		if parent := parentOf(p); parent != "" && deleted[parent] && !readded[parent] {
			continue // reported via the deleted parent
		}
		changes = append(changes, Change{Path: p, Kind: Deleted, Dir: wasDir})
	}

	for _, e := range l.Entries {
		if e.Whiteout || e.Opaque {
			continue
		}
		prev, existed := fsys[e.Path]
		existed = existed && !deleted[e.Path]
		fsys[e.Path] = node{dir: e.Dir, link: e.Symlink}
		c := Change{
			Path: e.Path, Size: e.Size, Dir: e.Dir, Mode: e.Mode, UID: e.UID, GID: e.GID,
			Symlink: e.Symlink, Hardlink: e.Hardlink, Preview: e.Preview, Content: e.Content,
		}
		switch {
		case !existed:
			c.Kind = Added
		case e.Dir && prev.dir:
			continue // parent directory re-listed; not a change on its own
		default:
			c.Kind = Modified
		}
		changes = append(changes, c)
	}

	// Resolve links once the whole layer is applied.
	for i := range changes {
		if c := &changes[i]; c.Symlink != "" {
			c.Resolved, c.Broken = resolve(fsys, linkTarget(c.Path, c.Symlink))
		}
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

// linkTarget turns a symlink's target into an image path.
func linkTarget(linkPath, target string) string {
	if !strings.HasPrefix(target, "/") {
		target = path.Join(path.Dir("/"+linkPath), target)
	}
	return strings.TrimPrefix(path.Clean("/"+target), "/")
}

// resolve follows symlinks in every component of p. It returns the final
// path and whether it is broken (missing or looping).
func resolve(fsys map[string]node, p string) (string, bool) {
	for hops := 0; hops <= maxLinkHops; hops++ {
		next, followed, ok := step(fsys, p)
		if !ok {
			return next, true
		}
		if !followed {
			return next, false
		}
		p = next
	}
	return p, true
}

// step walks p and substitutes the first symlink it meets. followed is false
// when p has no symlinks left; ok is false when a component is missing.
func step(fsys map[string]node, p string) (next string, followed, ok bool) {
	if p == "" {
		return "", false, true // the root always exists
	}
	parts := strings.Split(p, "/")
	cur := ""
	for i, part := range parts {
		cur = path.Join(cur, part)
		n, exists := fsys[cur]
		if !exists {
			return p, false, false
		}
		if n.link != "" {
			rest := path.Join(parts[i+1:]...)
			return path.Join(linkTarget(cur, n.link), rest), true, true
		}
	}
	return p, false, true
}

func under(fsys map[string]node, dir string) []string {
	prefix := dir + "/"
	var out []string
	for p := range fsys {
		if strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	return out
}

func parentOf(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return ""
	}
	return p[:i]
}
