package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/pkg/humanize"
)

// summary describes an image, or what it would look like after a plan.
type summary struct {
	Layers int    `json:"layers"`
	Size   int64  `json:"size"`
	Wasted wasted `json:"wasted"`
	steps  []image.LayerDetail
	hidden []image.WastedFile
}

type wasted struct {
	Bytes int64 `json:"bytes"`
	Files int   `json:"files"`
}

func summarize(details []image.LayerDetail) summary {
	s := summary{steps: details, hidden: image.Wasted(details)}
	for _, d := range details {
		if !d.Layer().Empty() {
			s.Layers++
			s.Size += d.Layer().Size().Bytes()
		}
	}
	s.Wasted = wasted{Bytes: image.WastedTotal(s.hidden).Bytes(), Files: len(s.hidden)}
	return s
}

func (s summary) String() string {
	return fmt.Sprintf("%s, %s, wasted %s in %s",
		plural(s.Layers, "layer"), humanize.Bytes(s.Size), humanize.Bytes(s.Wasted.Bytes), plural(s.Wasted.Files, "file"))
}

type stepJSON struct {
	Step    int    `json:"step"`
	Command string `json:"command"`
	Size    int64  `json:"size"`
	Empty   bool   `json:"empty,omitempty"`
	Edited  bool   `json:"edited,omitempty"`
	Merged  int    `json:"merged,omitempty"`
}

type wastedFileJSON struct {
	Step     int    `json:"step"`
	HiddenBy int    `json:"hidden_by"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
}

func stepsJSON(details []image.LayerDetail) []stepJSON {
	out := make([]stepJSON, len(details))
	for i, d := range details {
		out[i] = stepJSON{
			Step: i + 1, Command: d.Layer().CreatedBy(), Size: d.Layer().Size().Bytes(),
			Empty: d.Layer().Empty(), Edited: d.Edited(),
		}
		if d.Merged() > 1 {
			out[i].Merged = d.Merged()
		}
	}
	return out
}

func wastedJSON(w []image.WastedFile) []wastedFileJSON {
	out := make([]wastedFileJSON, len(w))
	for i, f := range w {
		out[i] = wastedFileJSON{Step: f.Step + 1, HiddenBy: f.By + 1, Path: "/" + f.Path, Size: f.Size.Bytes()}
	}
	return out
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func imageName(img image.Image, fallback string) string {
	if refs := img.References(); len(refs) > 0 {
		return refs[0].String()
	}
	return fallback
}

// printWasted lists the largest hidden files, at most limit of them
// (0 means all).
func printWasted(w io.Writer, hidden []image.WastedFile, limit int) {
	if len(hidden) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "SIZE\tLAYER\tHIDDEN BY\t PATH")
	shown := hidden
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	for _, f := range shown {
		fmt.Fprintf(tw, "%s\t%d\t%d\t /%s\n", f.Size, f.Step+1, f.By+1, f.Path)
	}
	_ = tw.Flush()
	if len(shown) < len(hidden) {
		fmt.Fprintf(w, "… and %d more (--all to list them)\n", len(hidden)-len(shown))
	}
}

func oneLine(s string, width int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > width {
		return s[:width-1] + "…"
	}
	return s
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
