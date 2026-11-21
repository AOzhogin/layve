package cli

import (
	"context"
	"fmt"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/pkg/humanize"
)

func runCheck(e *env, args []string) int {
	var c common
	maxWasted, maxLayers := "", 0
	fs := newFlags("check", "check IMAGE --max-wasted SIZE [--max-layers N] [flags]", e, &c)
	fs.StringVar(&maxWasted, "max-wasted", "", "largest acceptable wasted space, e.g. 10MB or 0")
	fs.IntVar(&maxLayers, "max-layers", 0, "largest acceptable number of layers (0: no limit)")
	name, code, ok := parseImageArgs(fs, args)
	if !ok {
		return code
	}
	limit := int64(-1)
	if maxWasted != "" {
		v, err := humanize.ParseBytes(maxWasted)
		if err != nil {
			return e.fail(fmt.Errorf("--max-wasted: %w", err))
		}
		limit = v
	}
	if limit < 0 && maxLayers <= 0 {
		return e.fail(fmt.Errorf("give --max-wasted and/or --max-layers"))
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

	var problems []string
	if limit >= 0 && s.Wasted.Bytes > limit {
		problems = append(problems, fmt.Sprintf("wasted %s exceeds %s", humanize.Bytes(s.Wasted.Bytes), humanize.Bytes(limit)))
	}
	if maxLayers > 0 && s.Layers > maxLayers {
		problems = append(problems, fmt.Sprintf("%d layers exceed %d", s.Layers, maxLayers))
	}
	if c.json {
		if err := writeJSON(e.stdout, struct {
			Image    string           `json:"image"`
			OK       bool             `json:"ok"`
			Problems []string         `json:"problems,omitempty"`
			Layers   int              `json:"layers"`
			Wasted   wasted           `json:"wasted"`
			Files    []wastedFileJSON `json:"wasted_files"`
		}{imageName(img, name), len(problems) == 0, problems, s.Layers, s.Wasted, wastedJSON(s.hidden)}); err != nil {
			return e.fail(err)
		}
	} else {
		status := "ok"
		if len(problems) > 0 {
			status = "FAILED: " + joinProblems(problems)
		}
		fmt.Fprintf(e.stdout, "%s: %s — %s\n", imageName(img, name), s, status)
		if len(problems) > 0 && len(s.hidden) > 0 {
			fmt.Fprintln(e.stdout)
			printWasted(e.stdout, s.hidden, 10)
			fmt.Fprintf(e.stdout, "\nFix it with: layve fix %s --wasted\n", name)
		}
	}
	if len(problems) > 0 {
		return ExitCheckFailed
	}
	return ExitOK
}

func joinProblems(p []string) string {
	out := p[0]
	for _, x := range p[1:] {
		out += "; " + x
	}
	return out
}
