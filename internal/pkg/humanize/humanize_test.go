package humanize

import (
	"io/fs"
	"testing"
	"time"
)

func TestBytes(t *testing.T) {
	cases := map[int64]string{
		0:             "0B",
		999:           "999B",
		1000:          "1kB",
		187_654_321:   "188MB",
		1_234_567_890: "1.23GB",
	}
	for in, want := range cases {
		if got := Bytes(in); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestSince(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{10 * time.Second, "less than a minute ago"},
		{time.Minute, "1 minute ago"},
		{5 * time.Hour, "5 hours ago"},
		{3 * 24 * time.Hour, "3 days ago"},
		{14 * 24 * time.Hour, "2 weeks ago"},
		{60 * 24 * time.Hour, "2 months ago"},
		{800 * 24 * time.Hour, "2 years ago"},
	}
	for _, c := range cases {
		if got := Since(now.Add(-c.ago), now); got != c.want {
			t.Errorf("Since(-%v) = %q, want %q", c.ago, got, c.want)
		}
	}
}

func TestFileMode(t *testing.T) {
	cases := map[fs.FileMode]string{
		0o644:                              "-rw-r--r--",
		fs.ModeDir | 0o755:                 "drwxr-xr-x",
		fs.ModeDir | fs.ModeSticky | 0o777: "drwxrwxrwt",
		fs.ModeSymlink | 0o777:             "lrwxrwxrwx",
		fs.ModeSetuid | 0o4755&0o777:       "-rwsr-xr-x",
		fs.ModeSetgid | 0o644:              "-rw-r-Sr--",
		fs.ModeDir | fs.ModeSticky | 0o776: "drwxrwxrwT",
	}
	for in, want := range cases {
		if got := FileMode(in); got != want {
			t.Errorf("FileMode(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestParseBytes(t *testing.T) {
	cases := map[string]int64{
		"512": 512, "512B": 512, "10MB": 10_000_000, "10m": 10_000_000, "1.5GB": 1_500_000_000,
		"64KiB": 65536, "1 GiB": 1 << 30, "5mb": 5_000_000, "0": 0,
	}
	for in, want := range cases {
		got, err := ParseBytes(in)
		if err != nil || got != want {
			t.Errorf("ParseBytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "MB", "-1MB", "10XB", "1.2.3MB"} {
		if _, err := ParseBytes(bad); err == nil {
			t.Errorf("ParseBytes(%q) accepted", bad)
		}
	}
}

func TestCount(t *testing.T) {
	cases := map[int64]string{0: "0", 950: "950", 12_345: "12.3k", 1_088_881_702: "1.09B", 92_683_082: "92.7M", 13_616_996_008: "13.6B"}
	for in, want := range cases {
		if got := Count(in); got != want {
			t.Errorf("Count(%d) = %q, want %q", in, got, want)
		}
	}
}
