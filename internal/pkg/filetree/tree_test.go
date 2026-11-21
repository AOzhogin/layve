package filetree

import (
	"strings"
	"testing"
)

type item struct {
	path string
	dir  bool
}

func describe(i item) (string, bool) { return i.path, i.dir }

func render(lines []Line[item]) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Prefix + l.Node.Name + "\n")
	}
	return b.String()
}

func TestBuildAndVisible(t *testing.T) {
	root := Build([]item{
		{path: "usr/bin/curl"},
		{path: "etc/hosts"},
		{path: "app", dir: true},
		{path: "usr/lib/libc.so"},
		{path: "README"},
	}, describe)
	root.SetExpanded(true)

	want := "" +
		"├── app\n" +
		"├── etc\n" +
		"│   └── hosts\n" +
		"├── usr\n" +
		"│   ├── bin\n" +
		"│   │   └── curl\n" +
		"│   └── lib\n" +
		"│       └── libc.so\n" +
		"└── README\n"
	if got := render(root.Visible()); got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}
	if root.Count() != 9 {
		t.Errorf("Count = %d", root.Count())
	}
}

func TestCollapse(t *testing.T) {
	root := Build([]item{{path: "a/b/c"}, {path: "a/d"}}, describe)
	root.ExpandToDepth(1)
	if got := render(root.Visible()); got != "└── a\n    ├── b\n    └── d\n" {
		t.Errorf("depth 1:\n%s", got)
	}
	root.ExpandToDepth(0)
	if got := len(root.Visible()); got != 1 {
		t.Errorf("collapsed lines = %d", got)
	}
}

func TestValues(t *testing.T) {
	root := Build([]item{{path: "/x/y/"}}, describe)
	var implied, explicit *Node[item]
	root.Walk(func(n *Node[item]) {
		switch n.Path {
		case "x":
			implied = n
		case "x/y":
			explicit = n
		}
	})
	if implied == nil || implied.Value != nil || !implied.Dir() {
		t.Errorf("implied dir node = %+v", implied)
	}
	if explicit == nil || explicit.Value == nil || explicit.Value.path != "/x/y/" {
		t.Errorf("explicit node = %+v", explicit)
	}
}
