package image

import (
	"errors"
	"testing"
)

func TestEditPlan(t *testing.T) {
	rm, err := RemoveFromLayer(2, "/etc/./app.conf")
	if err != nil {
		t.Fatal(err)
	}
	p := EditPlan{}.With(DropLayer(4)).With(rm)
	q := p.Undo()
	if p.Len() != 2 || q.Len() != 1 || p.Edits()[1].Path() != "etc/app.conf" {
		t.Fatalf("plan = %v, undo = %v", p.Edits(), q.Edits())
	}
	if got := MergeLayers(5, 2).String(); got != "merge layers 3–6" {
		t.Errorf("String = %q", got)
	}
	if _, err := PurgePath("/"); !errors.Is(err, ErrInvalidEdit) {
		t.Errorf("purge root err = %v", err)
	}
	content := []byte("x")
	put, _ := PutFile(0, "a", content, 0o100755, 0, 0)
	content[0] = 'y'
	if c, _, mode, _, _, size := put.File(); string(c) != "x" || mode != 0o755 || size != 1 {
		t.Errorf("put = %q %v %d", c, mode, size)
	}
}
