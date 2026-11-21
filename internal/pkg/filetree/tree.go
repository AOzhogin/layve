// Package filetree builds a collapsible tree from slash-separated paths and
// flattens its visible part for rendering.
package filetree

import (
	"sort"
	"strings"
)

// Node is a path component. Value is nil for directories that are only
// implied by deeper paths.
type Node[T any] struct {
	Name     string
	Path     string
	Value    *T
	Children []*Node[T]
	Expanded bool
	dir      bool
}

// Dir reports whether the node is a directory.
func (n *Node[T]) Dir() bool { return n.dir || len(n.Children) > 0 }

// Build creates a tree from items. describe returns the item's path and
// whether it is a directory. The returned root has an empty Path and is
// always expanded.
func Build[T any](items []T, describe func(T) (path string, dir bool)) *Node[T] {
	root := &Node[T]{Expanded: true, dir: true}
	index := map[string]*Node[T]{"": root}
	for i := range items {
		p, dir := describe(items[i])
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}
		n := ensure(index, p)
		n.Value = &items[i]
		n.dir = n.dir || dir
	}
	sortTree(root)
	return root
}

func ensure[T any](index map[string]*Node[T], p string) *Node[T] {
	if n, ok := index[p]; ok {
		return n
	}
	parentPath, name := "", p
	if i := strings.LastIndex(p, "/"); i >= 0 {
		parentPath, name = p[:i], p[i+1:]
	}
	parent := ensure(index, parentPath)
	parent.dir = true
	n := &Node[T]{Name: name, Path: p}
	parent.Children = append(parent.Children, n)
	index[p] = n
	return n
}

func sortTree[T any](n *Node[T]) {
	sort.Slice(n.Children, func(i, j int) bool {
		a, b := n.Children[i], n.Children[j]
		if a.Dir() != b.Dir() {
			return a.Dir()
		}
		return a.Name < b.Name
	})
	for _, c := range n.Children {
		sortTree(c)
	}
}

// Walk visits every node below n in depth-first order.
func (n *Node[T]) Walk(fn func(*Node[T])) {
	for _, c := range n.Children {
		fn(c)
		c.Walk(fn)
	}
}

// Count returns the number of nodes below n.
func (n *Node[T]) Count() int {
	total := 0
	n.Walk(func(*Node[T]) { total++ })
	return total
}

// ExpandToDepth expands directories shallower than depth and collapses the
// rest. Depth 0 collapses everything below n.
func (n *Node[T]) ExpandToDepth(depth int) {
	var visit func(*Node[T], int)
	visit = func(node *Node[T], d int) {
		for _, c := range node.Children {
			c.Expanded = d < depth
			visit(c, d+1)
		}
	}
	visit(n, 0)
}

// SetExpanded expands or collapses every directory below n.
func (n *Node[T]) SetExpanded(expanded bool) {
	n.Walk(func(c *Node[T]) { c.Expanded = expanded })
}

// Line is one visible row of the flattened tree.
type Line[T any] struct {
	Node *Node[T]
	// Prefix holds the box-drawing guides, e.g. "│   ├── ".
	Prefix string
	Depth  int
}

// Visible flattens the expanded part of the tree below n.
func (n *Node[T]) Visible() []Line[T] {
	var out []Line[T]
	var visit func(*Node[T], string, int)
	visit = func(node *Node[T], indent string, depth int) {
		for i, c := range node.Children {
			last := i == len(node.Children)-1
			branch, next := "├── ", "│   "
			if last {
				branch, next = "└── ", "    "
			}
			out = append(out, Line[T]{Node: c, Prefix: indent + branch, Depth: depth})
			if c.Dir() && c.Expanded {
				visit(c, indent+next, depth+1)
			}
		}
	}
	visit(n, "", 0)
	return out
}
