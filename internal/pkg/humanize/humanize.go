// Package humanize formats values for human-readable terminal output.
package humanize

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"
)

var units = []string{"B", "kB", "MB", "GB", "TB", "PB"}

// Bytes formats n using decimal (SI) units, matching the docker CLI.
func Bytes(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%dB", n)
	}
	v := float64(n)
	i := 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	return fmt.Sprintf("%.3g%s", v, units[i])
}

// Since describes how long ago t was relative to now, e.g. "3 days ago".
func Since(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 0:
		return "in the future"
	case d < time.Minute:
		return "less than a minute ago"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour")
	case d < 7*24*time.Hour:
		return plural(int(d/(24*time.Hour)), "day")
	case d < 30*24*time.Hour:
		return plural(int(d/(7*24*time.Hour)), "week")
	case d < 365*24*time.Hour:
		return plural(int(d/(30*24*time.Hour)), "month")
	default:
		return plural(int(d/(365*24*time.Hour)), "year")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s ago", unit)
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}

// FileMode formats m the way "ls -l" does: a type character followed by
// nine permission characters, with s/S and t/T for setuid, setgid and
// sticky bits. The result is always 10 characters wide.
func FileMode(m fs.FileMode) string {
	b := []byte("-rwxrwxrwx")
	switch {
	case m&fs.ModeDir != 0:
		b[0] = 'd'
	case m&fs.ModeSymlink != 0:
		b[0] = 'l'
	case m&fs.ModeNamedPipe != 0:
		b[0] = 'p'
	case m&fs.ModeSocket != 0:
		b[0] = 's'
	case m&fs.ModeCharDevice != 0:
		b[0] = 'c'
	case m&fs.ModeDevice != 0:
		b[0] = 'b'
	}
	for i := range 9 {
		if m&(1<<uint(8-i)) == 0 {
			b[i+1] = '-'
		}
	}
	special := func(set bool, pos int, lower, upper byte) {
		if !set {
			return
		}
		if b[pos] == 'x' {
			b[pos] = lower
		} else {
			b[pos] = upper
		}
	}
	special(m&fs.ModeSetuid != 0, 3, 's', 'S')
	special(m&fs.ModeSetgid != 0, 6, 's', 'S')
	special(m&fs.ModeSticky != 0, 9, 't', 'T')
	return string(b)
}

// ParseBytes reads sizes such as "512", "10MB", "1.5GB" or "64KiB". Decimal
// units (kB, MB, …) use powers of 1000, binary ones (KiB, MiB, …) 1024;
// case does not matter and a "B" suffix is optional ("10m" is 10MB).
func ParseBytes(s string) (int64, error) {
	t := strings.TrimSpace(s)
	i := 0
	for i < len(t) && (t[i] >= '0' && t[i] <= '9' || t[i] == '.') {
		i++
	}
	num, unit := t[:i], strings.ToLower(strings.TrimSpace(t[i:]))
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	mult, ok := sizeUnits[strings.TrimSuffix(unit, "b")]
	if !ok && unit != "b" {
		return 0, fmt.Errorf("invalid size unit in %q", s)
	}
	if unit == "b" {
		mult = 1
	}
	return int64(v * float64(mult)), nil
}

var sizeUnits = map[string]int64{
	"": 1, "k": 1e3, "m": 1e6, "g": 1e9, "t": 1e12,
	"ki": 1 << 10, "mi": 1 << 20, "gi": 1 << 30, "ti": 1 << 40,
}

// Count abbreviates large counts: 950, 12.3k, 1.09M, 13.6B.
func Count(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1e6:
		return fmt.Sprintf("%.3gk", float64(n)/1e3)
	case n < 1e9:
		return fmt.Sprintf("%.3gM", float64(n)/1e6)
	default:
		return fmt.Sprintf("%.3gB", float64(n)/1e9)
	}
}
