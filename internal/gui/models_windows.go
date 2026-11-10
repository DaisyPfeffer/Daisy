//go:build windows

package gui

import "github.com/lxn/walk"

// dlModel feeds the main download list.
type dlModel struct {
	walk.TableModelBase
	rows []Row
}

func (m *dlModel) RowCount() int { return len(m.rows) }

func (m *dlModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	c := m.rows[row].Cell(col)
	if col >= 2 && col <= 5 {
		return Localize(c)
	}
	return c
}

// connModel feeds the connections table of a progress window.
type connModel struct {
	walk.TableModelBase
	rows []ConnRow
}

func (m *connModel) RowCount() int { return len(m.rows) }

func (m *connModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	r := m.rows[row]
	switch col {
	case 0:
		return r.N
	case 1:
		return r.Downloaded
	default:
		return T(r.Info)
	}
}

// catItem is a node in the Categories tree.
type catItem struct {
	cat      Category
	parent   *catItem
	children []*catItem
}

func (c *catItem) Text() string { return T(c.cat.Title()) }

func (c *catItem) Parent() walk.TreeItem {
	if c.parent == nil {
		return nil
	}
	return c.parent
}

func (c *catItem) ChildCount() int { return len(c.children) }

func (c *catItem) ChildAt(i int) walk.TreeItem { return c.children[i] }

func (c *catItem) Image() interface{} {
	if b := bitmap(c.cat.IconName()); b != nil {
		return b
	}
	return nil
}

type catModel struct {
	walk.TreeModelBase
	roots []*catItem
}

func newCatModel() *catModel {
	all := &catItem{cat: CatAll}
	for _, c := range []Category{CatCompressed, CatDocuments, CatMusic, CatPrograms, CatVideo} {
		all.children = append(all.children, &catItem{cat: c, parent: all})
	}
	return &catModel{roots: []*catItem{all, {cat: CatUnfinished}, {cat: CatFinished}, {cat: CatQueue}}}
}

func (m *catModel) LazyPopulation() bool { return false }

func (m *catModel) RootCount() int { return len(m.roots) }

func (m *catModel) RootAt(i int) walk.TreeItem { return m.roots[i] }
