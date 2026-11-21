package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/moby/moby/client"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/pkg/imagearchive"
)

// Inspection is an image read from the daemon, ready to preview edits.
type Inspection struct {
	history []image.Layer
	archive *imagearchive.Archive
}

// Inspect streams the image out of the daemon and keeps its file metadata
// and small text files in memory. progress receives the number of bytes
// read so far and may be nil.
func (r *Images) Inspect(ctx context.Context, id image.ID, progress func(read int64)) (*Inspection, error) {
	history, err := r.History(ctx, id)
	if err != nil {
		return nil, err
	}
	stream, err := r.save(ctx, []string{id.String()})
	if err != nil {
		return nil, fmt.Errorf("save %s: %w", id.Short(), err)
	}
	defer stream.Close()

	var src io.Reader = stream
	if progress != nil {
		src = &countingReader{r: stream, report: progress}
	}
	archive, err := imagearchive.Read(src, imagearchive.DefaultOptions)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", id.Short(), err)
	}
	return &Inspection{history: history, archive: archive}, nil
}

// Details shows the image as it would look after plan: every history step
// with the files its layer changes.
func (in *Inspection) Details(plan image.EditPlan) ([]image.LayerDetail, error) {
	return details(in.archive, in.history, plan)
}

func details(a *imagearchive.Archive, history []image.Layer, plan image.EditPlan) ([]image.LayerDetail, error) {
	state, err := applyPlan(a, plan)
	if err != nil {
		return nil, err
	}
	if err := state.Validate(); err != nil {
		return nil, err
	}
	layers := state.Layers()
	diffs := imagearchive.Diff(layers)
	// The API history has sizes and lines up with the archive history.
	aligned := len(history) == len(a.History)

	var out []image.LayerDetail
	for _, st := range state.Steps() {
		var changes []image.FileChange
		size := image.Size{}
		if st.Layer >= 0 {
			if changes, err = toChanges(diffs[st.Layer]); err != nil {
				return nil, err
			}
			bytes := imagearchive.LayerSize(layers[st.Layer])
			if aligned && !st.Edited {
				bytes = history[st.Orig].Size().Bytes()
			}
			size, _ = image.NewSize(bytes)
		}
		createdBy, created := st.CreatedBy, st.Created
		if aligned {
			createdBy, created = history[st.Orig].CreatedBy(), history[st.Orig].Created()
		}
		layer := image.NewLayer(createdBy, size, created, st.EmptyLayer)
		out = append(out, image.NewLayerDetail(layer, changes).WithEdit(st.Edited, st.Merged))
	}
	return out, nil
}

// applyPlan replays the edits on a fresh state.
func applyPlan(a *imagearchive.Archive, plan image.EditPlan) (*imagearchive.State, error) {
	s := imagearchive.NewState(a)
	for _, e := range plan.Edits() {
		if err := applyEdit(s, e); err != nil {
			return nil, fmt.Errorf("%s: %w", e, err)
		}
	}
	return s, nil
}

func applyEdit(s *imagearchive.State, e image.Edit) error {
	from, to := e.Steps()
	switch e.Kind() {
	case image.EditDropLayer:
		return s.DropLayer(from)
	case image.EditMergeLayers:
		return s.MergeLayers(from, to)
	case image.EditRemoveFromLayer:
		return s.RemovePath(from, e.Path())
	case image.EditPurgePath:
		return s.PurgePath(e.Path())
	case image.EditPutFile:
		content, host, mode, uid, gid, size := e.File()
		if host != "" {
			content = nil
		}
		return s.PutFile(from, imagearchive.NewFile{
			Path: e.Path(), Mode: mode, UID: uid, GID: gid, Content: content, HostPath: host, Size: size,
		})
	default:
		return fmt.Errorf("unknown edit kind %d", e.Kind())
	}
}

// Write saves the image with plan applied under ref, leaving the original
// untouched. progress receives human-readable stages and may be nil.
func (r *Images) Write(ctx context.Context, id image.ID, plan image.EditPlan, ref image.Reference, opts image.WriteOptions, progress func(stage string)) error {
	report := func(s string) {
		if progress != nil {
			progress(s)
		}
	}
	dir, err := os.MkdirTemp("", "layve-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	report("exporting image")
	stream, err := r.save(ctx, []string{id.String()})
	if err != nil {
		return fmt.Errorf("save %s: %w", id.Short(), err)
	}
	// Contents are copied from the extracted layers, so no previews are kept.
	stored, err := imagearchive.Extract(stream, dir, imagearchive.Options{})
	_ = stream.Close()
	if err != nil {
		return fmt.Errorf("extract %s: %w", id.Short(), err)
	}
	state, err := applyPlan(stored.Archive, plan)
	if err != nil {
		return err
	}
	if err := state.Validate(); err != nil {
		return err
	}

	pr, pw := io.Pipe()
	buildErr := make(chan error, 1)
	go func() {
		var bopts imagearchive.BuildOptions
		if opts.MarkHistory {
			bopts.HistoryComment = image.HistoryMark
		}
		err := imagearchive.Build(stored, state, ref.String(), pw, bopts, func(s imagearchive.Stage) {
			report(fmt.Sprintf("writing layer %d/%d", s.Layer, s.Layers))
		})
		pw.CloseWithError(err)
		buildErr <- err
	}()

	res, err := r.cli.ImageLoad(ctx, pr, client.ImageLoadWithQuiet(true))
	if err != nil {
		_ = pr.CloseWithError(err)
		if berr := <-buildErr; berr != nil {
			return fmt.Errorf("build image: %w", berr)
		}
		return fmt.Errorf("load image: %w", err)
	}
	defer res.Close()
	loadErr := readLoadResponse(res)
	if berr := <-buildErr; berr != nil {
		return fmt.Errorf("build image: %w", berr)
	}
	if loadErr != nil {
		return fmt.Errorf("load image: %w", loadErr)
	}
	report("done")
	return nil
}

// readLoadResponse scans the JSON message stream of an image load for
// errors.
func readLoadResponse(r io.Reader) error {
	dec := json.NewDecoder(r)
	for {
		var msg struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		err := dec.Decode(&msg)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if msg.ErrorDetail.Message != "" {
			return errors.New(msg.ErrorDetail.Message)
		}
		if msg.Error != "" {
			return errors.New(msg.Error)
		}
	}
}
