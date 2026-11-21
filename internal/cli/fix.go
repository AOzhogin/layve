package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/AOzhogin/layve/internal/domain/image"
)

type fixOptions struct {
	wasted bool
	purge  stringList
	merge  stringList
	drop   stringList
	squash bool
}

func runFix(e *env, args []string) int {
	var (
		c      common
		o      fixOptions
		output string
		dryRun bool
		noMark bool
	)
	fs := newFlags("fix", "fix IMAGE [--wasted] [--purge PATH]... [--squash | --merge A-B]... [--drop N]... [-o TAG] [flags]", e, &c)
	fs.BoolVar(&o.wasted, "wasted", false, "remove file versions hidden by later layers")
	fs.Var(&o.purge, "purge", "remove `PATH` from every layer (repeatable), e.g. a leaked secret")
	fs.Var(&o.merge, "merge", "merge the layers of steps `A-B` into one (repeatable; step numbers as in inspect)")
	fs.BoolVar(&o.squash, "squash", false, "merge all layers into one")
	fs.Var(&o.drop, "drop", "drop the layer of step `N` (repeatable)")
	fs.StringVar(&output, "o", "", "write the result as `TAG` (default: overwrite IMAGE's tag)")
	fs.BoolVar(&dryRun, "dry-run", false, "show what would change without writing anything")
	fs.BoolVar(&noMark, "no-mark", false, `do not note "`+image.HistoryMark+`" in the history`)
	name, code, ok := parseImageArgs(fs, args)
	if !ok {
		return code
	}
	if !o.wasted && !o.squash && len(o.purge)+len(o.merge)+len(o.drop) == 0 {
		return e.fail(fmt.Errorf("nothing to do: give --wasted, --purge, --squash, --merge or --drop"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	svc, img, in, err := e.load(ctx, c, name)
	if err != nil {
		return e.fail(err)
	}
	defer svc.Close()

	target, err := fixTarget(img, name, output)
	if err != nil {
		return e.fail(err)
	}
	base, err := in.Details(image.EditPlan{})
	if err != nil {
		return e.fail(err)
	}
	plan, notes, err := buildPlan(in, base, o)
	if err != nil {
		return e.fail(err)
	}
	after, err := in.Details(plan)
	if err != nil {
		return e.fail(err)
	}
	before, result := summarize(base), summarize(after)

	written := false
	if plan.Len() > 0 && !dryRun {
		progress := func(stage string) {
			if !c.json {
				fmt.Fprintf(e.stderr, "layve: %s\n", stage)
			}
		}
		opts := image.WriteOptions{MarkHistory: !noMark}
		if err := svc.Write(ctx, img.ID(), plan, target, opts, progress); err != nil {
			return e.fail(err)
		}
		written = true
	}
	newID := ""
	if written {
		if out, err := svc.Find(ctx, target.String()); err == nil {
			newID = out.ID().String()
		}
	}

	if c.json {
		if err := writeJSON(e.stdout, struct {
			Image      string   `json:"image"`
			Target     string   `json:"target"`
			DryRun     bool     `json:"dry_run"`
			Written    bool     `json:"written"`
			Edits      []string `json:"edits"`
			Before     summary  `json:"before"`
			After      summary  `json:"after"`
			ID         string   `json:"id,omitempty"`
			PreviousID string   `json:"previous_id"`
		}{imageName(img, name), target.String(), dryRun, written, describe(plan), before, result, newID, img.ID().String()}); err != nil {
			return e.fail(err)
		}
		return ExitOK
	}

	fmt.Fprintf(e.stdout, "%s (%s)\n", imageName(img, name), img.ID().Short())
	for _, n := range notes {
		fmt.Fprintf(e.stdout, "  %s\n", n)
	}
	fmt.Fprintf(e.stdout, "before: %s\nafter:  %s\n", before, result)
	switch {
	case plan.Len() == 0:
		fmt.Fprintln(e.stdout, "nothing to change")
	case dryRun:
		fmt.Fprintf(e.stdout, "dry run: would write %s\n", target)
	default:
		fmt.Fprintf(e.stdout, "wrote %s (%s)", target, shortID(newID))
		if overwrites(img, target) {
			fmt.Fprintf(e.stdout, "; the previous image is kept as %s", img.ID().Short())
		}
		fmt.Fprintln(e.stdout)
	}
	return ExitOK
}

// fixTarget is -o, else the tag IMAGE was given by, else the image's only tag.
func fixTarget(img image.Image, name, output string) (image.Reference, error) {
	if output != "" {
		return image.ParseReference(output)
	}
	refs := img.References()
	if given, err := image.ParseReference(name); err == nil {
		for _, r := range refs {
			if r == given {
				return r, nil
			}
		}
	}
	if len(refs) == 1 {
		return refs[0], nil
	}
	return image.Reference{}, fmt.Errorf("cannot tell which tag of %s to overwrite; give -o TAG", name)
}

func overwrites(img image.Image, target image.Reference) bool {
	for _, r := range img.References() {
		if r == target {
			return true
		}
	}
	return false
}

func shortID(id string) string {
	if parsed, err := image.NewID(id); err == nil {
		return parsed.Short()
	}
	return "unknown id"
}

// buildPlan applies the options in a fixed order: merges and drops use the
// step numbers of the original image, then purges, then wasted-space removal
// computed on the result of everything before it. It returns the plan and
// human-readable notes about it.
func buildPlan(in Inspection, base []image.LayerDetail, o fixOptions) (image.EditPlan, []string, error) {
	var plan image.EditPlan
	var notes []string
	try := func(e image.Edit) error {
		next := plan.With(e)
		if _, err := in.Details(next); err != nil {
			return fmt.Errorf("%s: %w", e, err)
		}
		plan = next
		notes = append(notes, e.String())
		return nil
	}

	merges := []string(o.merge)
	if o.squash {
		first, last := -1, -1
		for i, d := range base {
			if !d.Layer().Empty() {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		if first >= 0 && first != last {
			merges = append(merges, fmt.Sprintf("%d-%d", first+1, last+1))
		}
	}
	for _, m := range merges {
		from, to, err := parseRange(m, len(base))
		if err != nil {
			return plan, nil, fmt.Errorf("--merge %s: %w", m, err)
		}
		if err := try(image.MergeLayers(from, to)); err != nil {
			return plan, nil, err
		}
	}

	var drops []int
	for _, d := range o.drop {
		n, err := strconv.Atoi(strings.TrimSpace(d))
		if err != nil || n < 1 || n > len(base) {
			return plan, nil, fmt.Errorf("--drop %s: want a step number from 1 to %d", d, len(base))
		}
		drops = append(drops, n-1)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(drops))) // later steps first: no renumbering
	for _, d := range drops {
		if err := try(image.DropLayer(d)); err != nil {
			return plan, nil, err
		}
	}

	for _, p := range o.purge {
		e, err := image.PurgePath(p)
		if err != nil {
			return plan, nil, fmt.Errorf("--purge %s: %w", p, err)
		}
		if err := try(e); err != nil {
			return plan, nil, err
		}
	}

	if o.wasted {
		note, err := removeWasted(in, &plan)
		if err != nil {
			return plan, nil, err
		}
		notes = append(notes, note)
	}
	return plan, notes, nil
}

// removeWasted adds removals for every hidden file version. Hard link
// targets cannot be removed alone; those are skipped.
func removeWasted(in Inspection, plan *image.EditPlan) (string, error) {
	details, err := in.Details(*plan)
	if err != nil {
		return "", err
	}
	hidden := image.Wasted(details)
	if len(hidden) == 0 {
		return "no wasted space", nil
	}
	edits := make([]image.Edit, 0, len(hidden))
	for _, w := range hidden {
		if e, err := image.RemoveFromLayer(w.Step, w.Path); err == nil {
			edits = append(edits, e)
		}
	}
	if next := plan.WithAll(edits...); validPlan(in, next) {
		*plan = next
		return fmt.Sprintf("remove %d hidden file versions (%s)", len(edits), image.WastedTotal(hidden)), nil
	}
	ok := 0
	for _, e := range edits {
		if next := plan.With(e); validPlan(in, next) {
			*plan = next
			ok++
		}
	}
	return fmt.Sprintf("remove %d of %d hidden file versions; the rest are hard link targets", ok, len(edits)), nil
}

func validPlan(in Inspection, p image.EditPlan) bool {
	_, err := in.Details(p)
	return err == nil
}

func parseRange(s string, steps int) (int, int, error) {
	a, b, found := strings.Cut(s, "-")
	if !found {
		return 0, 0, fmt.Errorf("want A-B, e.g. 3-7")
	}
	from, err1 := strconv.Atoi(strings.TrimSpace(a))
	to, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil || from < 1 || to > steps || from >= to {
		return 0, 0, fmt.Errorf("want two step numbers from 1 to %d, first smaller", steps)
	}
	return from - 1, to - 1, nil
}

func describe(p image.EditPlan) []string {
	out := []string{}
	for _, e := range p.Edits() {
		out = append(out, e.String())
	}
	return out
}
