package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AOzhogin/layve/internal/domain/image"
)

// row is one table line: an image seen through one of its tags, or through
// its ID when it has none.
type row struct {
	img    image.Image
	target image.Target
}

func (r row) key() string { return r.target.String() }

// retagStep moves one row to a new reference.
type retagStep struct {
	from row
	to   image.Reference
}

var errNothingToDo = errors.New("nothing to change")

// planRetag turns user input into steps. For one row the input is a full
// reference; for several it is only the new tag, applied to each row's own
// repository.
func planRetag(rows []row, input string) ([]retagStep, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, errors.New("enter a new name")
	}
	steps := make([]retagStep, 0, len(rows))
	seen := map[image.Reference]bool{}
	for _, r := range rows {
		raw := input
		if len(rows) > 1 {
			old, tagged := r.target.Reference()
			if !tagged {
				return nil, fmt.Errorf("%s has no repository; retag it on its own", r.target.Display())
			}
			if strings.ContainsAny(input, ":/") {
				return nil, errors.New("for several images enter only the tag")
			}
			raw = old.Repository() + ":" + input
		}
		ref, err := image.ParseReference(raw)
		if err != nil {
			return nil, err
		}
		if seen[ref] {
			return nil, fmt.Errorf("two images would both become %s", ref)
		}
		seen[ref] = true
		if old, tagged := r.target.Reference(); tagged && old == ref {
			continue
		}
		steps = append(steps, retagStep{from: r, to: ref})
	}
	if len(steps) == 0 {
		return nil, errNothingToDo
	}
	return steps, nil
}

// opResult summarizes a batch operation.
type opResult struct {
	verb string
	done int
	errs []error
}

func (r opResult) message() string {
	msg := fmt.Sprintf("%s %d", r.verb, r.done)
	switch len(r.errs) {
	case 0:
		return msg
	case 1:
		return fmt.Sprintf("%s, failed 1: %v", msg, r.errs[0])
	default:
		return fmt.Sprintf("%s, failed %d; first: %v", msg, len(r.errs), r.errs[0])
	}
}

func runRemove(ctx context.Context, svc ImageService, rows []row, force bool) opResult {
	res := opResult{verb: "Removed"}
	for _, r := range rows {
		if err := svc.Remove(ctx, r.target, force); err != nil {
			res.errs = append(res.errs, err)
			continue
		}
		res.done++
	}
	return res
}

// runRetag tags each image with its new reference, then drops the old tag.
// The image survives because the new tag still points at it.
func runRetag(ctx context.Context, svc ImageService, steps []retagStep) opResult {
	res := opResult{verb: "Retagged"}
	for _, s := range steps {
		if err := svc.Tag(ctx, image.IDTarget(s.from.img.ID()), s.to); err != nil {
			res.errs = append(res.errs, err)
			continue
		}
		if _, tagged := s.from.target.Reference(); tagged {
			if err := svc.Remove(ctx, s.from.target, false); err != nil {
				res.errs = append(res.errs, fmt.Errorf("tagged %s but kept old tag: %w", s.to, err))
				continue
			}
		}
		res.done++
	}
	return res
}
