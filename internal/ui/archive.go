package ui

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/AOzhogin/layve/internal/domain/image"
	"github.com/AOzhogin/layve/internal/pkg/humanize"
)

func (m ImagesModel) askSavePath() (ImagesModel, tea.Cmd) {
	if m.busyTransfer() {
		return m, nil
	}
	rows := m.targets()
	if len(rows) == 0 {
		return m, nil
	}
	m.pending = rows
	m.mode = modeSavePath
	m.input.SetValue(defaultArchiveName(rows))
	m.input.Placeholder = "images.tar (.tar.gz to compress)"
	m.input.CursorEnd()
	return m, m.input.Focus()
}

func (m ImagesModel) askLoadPath() (ImagesModel, tea.Cmd) {
	if m.busyTransfer() {
		return m, nil
	}
	m.mode = modeLoadPath
	m.input.SetValue("")
	m.input.Placeholder = "path to a .tar, .tar.gz or .tgz made by docker save"
	return m, m.input.Focus()
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// defaultArchiveName is "nginx_1.27.tar" for one tagged image, else
// "images.tar".
func defaultArchiveName(rows []row) string {
	if len(rows) == 1 {
		if ref, ok := rows[0].target.Reference(); ok {
			repo := ref.Repository()
			if i := strings.LastIndex(repo, "/"); i >= 0 {
				repo = repo[i+1:]
			}
			return unsafeName.ReplaceAllString(repo+"_"+ref.Tag(), "-") + ".tar"
		}
		return rows[0].img.ID().Short() + ".tar"
	}
	return "images.tar"
}

func (m ImagesModel) updatePathInput(msg tea.KeyPressMsg) (ImagesModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	case "enter":
		mode, value := m.mode, m.input.Value()
		m.mode = modeList
		m.input.Blur()
		path, err := expandPath(value)
		if err != nil {
			m.status, m.statusOK = err.Error(), false
			return m, nil
		}
		if mode == modeSavePath {
			return m.startSave(path, m.pending)
		}
		return m.startLoad(path)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func expandPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("enter a file name")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Abs(p)
}

func compressed(path string) bool {
	return strings.HasSuffix(path, ".gz") || strings.HasSuffix(path, ".tgz")
}

func (m ImagesModel) startSave(path string, rows []row) (ImagesModel, tea.Cmd) {
	if _, err := os.Stat(path); err == nil {
		m.status, m.statusOK = path+" already exists; choose another name", false
		return m, nil
	}
	targets, total := exportTargets(rows)
	m.selected = map[string]bool{}
	m.refreshTable()
	n, svc := len(targets), m.svc
	return m.runTransfer(transfer{
		label:  fmt.Sprintf("Saving %d image(s) to %s", n, path),
		done:   fmt.Sprintf("Saved %d image(s) to %s", n, path),
		failed: "save failed",
		total:  total,
	}, func(progress func(int64)) (int64, error) {
		return saveArchive(svc, targets, path, progress)
	})
}

// saveArchive writes next to path first and renames when complete, so a
// failed save never leaves a truncated archive behind.
func saveArchive(svc ImageService, targets []image.Target, path string, progress func(int64)) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), copyTimeout)
	defer cancel()
	rc, err := svc.Export(ctx, targets)
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	tmp, err := os.CreateTemp(filepath.Dir(path), ".layve-*.tar")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	var w io.Writer = tmp
	var gz *gzip.Writer
	if compressed(path) {
		gz = gzip.NewWriter(tmp)
		w = gz
	}
	counter := &progressReader{r: rc, report: progress}
	if _, err := io.Copy(w, counter); err != nil {
		_ = tmp.Close()
		return counter.read, err
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			_ = tmp.Close()
			return counter.read, err
		}
	}
	if err := tmp.Close(); err != nil {
		return counter.read, err
	}
	// CreateTemp makes 0600 files; an archive is an ordinary file the user
	// may hand to others, like "docker save -o" output.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { //nolint:gosec // G302: deliberate

		return counter.read, err
	}
	return counter.read, os.Rename(tmp.Name(), path)
}

func (m ImagesModel) startLoad(path string) (ImagesModel, tea.Cmd) {
	info, err := os.Stat(path)
	if err != nil {
		m.status, m.statusOK = err.Error(), false
		return m, nil
	}
	if !info.Mode().IsRegular() {
		m.status, m.statusOK = path+" is not a file", false
		return m, nil
	}
	where := "here"
	if m.context != "" {
		where = m.context
	}
	svc := m.svc
	return m.runTransfer(transfer{
		label:  fmt.Sprintf("Loading %s into %s", filepath.Base(path), where),
		done:   fmt.Sprintf("Loaded %s into %s", filepath.Base(path), where),
		failed: "load failed",
		total:  info.Size(),
		reload: true,
	}, func(progress func(int64)) (int64, error) {
		return loadArchive(svc, path, progress)
	})
}

// loadArchive streams a file into the daemon; Docker itself unpacks gzip,
// bzip2 and xz archives.
func loadArchive(svc ImageService, path string, progress func(int64)) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), copyTimeout)
	defer cancel()
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	counter := &progressReader{r: f, report: progress}
	return counter.read, svc.Import(ctx, counter)
}

func (m ImagesModel) pathView() string {
	title := "Load images from a file"
	if m.mode == modeSavePath {
		title = fmt.Sprintf("Save %d image(s) to a file (~%s; .tar.gz compresses)", len(m.pending), humanize.Bytes(sizeOf(m.pending)))
	}
	return dialogStyle.Render(title + "\n" + m.input.View())
}

func sizeOf(rows []row) int64 {
	_, total := exportTargets(rows)
	return total
}
