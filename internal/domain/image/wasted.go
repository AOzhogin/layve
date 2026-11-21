package image

import (
	"path"
	"sort"
)

// WastedFile is a file version kept in a lower layer although a later
// layer overwrites or deletes it: it takes space but is never visible.
type WastedFile struct {
	Step int // step holding the hidden version
	By   int // nearest later step that hides it
	Path string
	Size Size
}

// Wasted finds hidden file versions in an image's history, largest first.
func Wasted(details []LayerDetail) []WastedFile {
	changedBy := map[string]int{} // path -> nearest later step changing it
	deletedBy := map[string]int{} // path -> nearest later step deleting it
	var out []WastedFile
	for i := len(details) - 1; i >= 0; i-- {
		changes := details[i].Changes()
		for _, c := range changes {
			if c.Kind() == ChangeDeleted || c.Dir() || c.Size().Bytes() == 0 {
				continue
			}
			if by, ok := hiddenBy(c.Path(), changedBy, deletedBy); ok {
				out = append(out, WastedFile{Step: i, By: by, Path: c.Path(), Size: c.Size()})
			}
		}
		for _, c := range changes {
			changedBy[c.Path()] = i
			if c.Kind() == ChangeDeleted {
				deletedBy[c.Path()] = i
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size.Bytes() > out[j].Size.Bytes() })
	return out
}

func hiddenBy(p string, changedBy, deletedBy map[string]int) (int, bool) {
	if by, ok := changedBy[p]; ok {
		return by, true
	}
	for dir := path.Dir(p); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if by, ok := deletedBy[dir]; ok {
			return by, true
		}
	}
	return 0, false
}

// WastedTotal sums the sizes of wasted files.
func WastedTotal(w []WastedFile) Size {
	var total int64
	for _, f := range w {
		total += f.Size.Bytes()
	}
	s, _ := NewSize(total)
	return s
}
