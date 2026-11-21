package image

import (
	"testing"
	"time"
)

func fc(t *testing.T, p string, k ChangeKind, size int64, dir bool) FileChange {
	t.Helper()
	s, _ := NewSize(size)
	c, err := NewFileChange(p, k, s, dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWasted(t *testing.T) {
	step := func(changes ...FileChange) LayerDetail {
		return NewLayerDetail(NewLayer("x", Size{}, time.Time{}, false), changes)
	}
	details := []LayerDetail{
		step(fc(t, "etc/app.conf", ChangeAdded, 100, false), fc(t, "tmp/build", ChangeAdded, 5000, false), fc(t, "bin/app", ChangeAdded, 10, false)),
		NewLayerDetail(NewLayer("ENV", Size{}, time.Time{}, true), nil),
		step(fc(t, "etc/app.conf", ChangeModified, 120, false)),
		step(fc(t, "tmp", ChangeDeleted, 0, true), fc(t, "etc/app.conf", ChangeModified, 130, false)),
	}
	got := Wasted(details)
	if len(got) != 3 {
		t.Fatalf("wasted = %+v", got)
	}
	if got[0].Path != "tmp/build" || got[0].Step != 0 || got[0].By != 3 {
		t.Errorf("largest = %+v", got[0])
	}
	if got[1].Path != "etc/app.conf" || got[1].Step != 2 || got[1].By != 3 {
		t.Errorf("second = %+v", got[1])
	}
	if got[2].Step != 0 || got[2].By != 2 {
		t.Errorf("third = %+v (nearest later step expected)", got[2])
	}
	if WastedTotal(got).Bytes() != 5220 {
		t.Errorf("total = %d", WastedTotal(got).Bytes())
	}
}
