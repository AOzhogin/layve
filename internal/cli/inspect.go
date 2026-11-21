package cli

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/AOzhogin/layve/internal/domain/image"
)

func runInspect(e *env, args []string) int {
	var c common
	all := false
	fs := newFlags("inspect", "inspect IMAGE [flags]", e, &c)
	fs.BoolVar(&all, "all", false, "list every wasted file, not only the largest ten")
	name, code, ok := parseImageArgs(fs, args)
	if !ok {
		return code
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	svc, img, in, err := e.load(ctx, c, name)
	if err != nil {
		return e.fail(err)
	}
	defer svc.Close()
	details, err := in.Details(image.EditPlan{})
	if err != nil {
		return e.fail(err)
	}
	s := summarize(details)

	if c.json {
		err := writeJSON(e.stdout, struct {
			Image  string           `json:"image"`
			ID     string           `json:"id"`
			Layers int              `json:"layers"`
			Size   int64            `json:"size"`
			Steps  []stepJSON       `json:"steps"`
			Wasted wasted           `json:"wasted"`
			Files  []wastedFileJSON `json:"wasted_files"`
		}{imageName(img, name), img.ID().String(), s.Layers, s.Size, stepsJSON(details), s.Wasted, wastedJSON(s.hidden)})
		if err != nil {
			return e.fail(err)
		}
		return ExitOK
	}

	fmt.Fprintf(e.stdout, "%s (%s): %s\n\n", imageName(img, name), img.ID().Short(), s)
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STEP\tSIZE\tCOMMAND")
	for i, d := range details {
		size := d.Layer().Size().String()
		if d.Layer().Empty() {
			size = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\n", i+1, size, oneLine(d.Layer().CreatedBy(), 90))
	}
	if err := tw.Flush(); err != nil {
		return e.fail(err)
	}
	if len(s.hidden) > 0 {
		fmt.Fprintln(e.stdout)
		limit := 10
		if all {
			limit = 0
		}
		printWasted(e.stdout, s.hidden, limit)
	}
	return ExitOK
}
