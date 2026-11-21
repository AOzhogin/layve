package registry

import (
	"errors"
	"testing"
	"time"
)

func TestParsePlatform(t *testing.T) {
	p, err := ParsePlatform("linux/arm64/v8")
	if err != nil || p.OS() != "linux" || p.Arch() != "arm64" || p.Variant() != "v8" || p.String() != "linux/arm64/v8" {
		t.Fatalf("got %+v, %v", p, err)
	}
	for _, bad := range []string{"", "linux", "linux/", "a/b/c/d", "linux/arm 64"} {
		if _, err := ParsePlatform(bad); !errors.Is(err, ErrInvalidPlatform) {
			t.Errorf("ParsePlatform(%q) err = %v", bad, err)
		}
	}
}

func TestMatchesAndFind(t *testing.T) {
	amd, _ := ParsePlatform("linux/amd64")
	armv8, _ := ParsePlatform("linux/arm64/v8")
	arm, _ := ParsePlatform("linux/arm64")
	armv7, _ := ParsePlatform("linux/arm/v7")
	if !armv8.Matches(arm) || armv8.Matches(amd) {
		t.Error("Matches")
	}
	tag, err := NewTag("1.27", time.Now(), []Variant{{Platform: armv7}, {Platform: armv8, Size: 9}, {Platform: amd, Size: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if v := tag.Variants(); v[0].Platform != amd || v[2].Platform != armv8 {
		t.Errorf("not sorted: %+v", v)
	}
	if v, ok := tag.Find(arm); !ok || v.Size != 9 {
		t.Errorf("Find arm64 = %+v %v", v, ok)
	}
	s390, _ := ParsePlatform("linux/s390x")
	if _, ok := tag.Find(s390); ok {
		t.Error("found a missing platform")
	}
}

func TestConstructorsValidate(t *testing.T) {
	if _, err := NewRepository(" ", "", 0, 0, false); err == nil {
		t.Error("empty repository accepted")
	}
	if _, err := NewTag("", time.Time{}, nil); err == nil {
		t.Error("empty tag accepted")
	}
}
