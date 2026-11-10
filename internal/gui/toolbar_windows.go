//go:build windows

package gui

import (
	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

// toolButton is a big "icon above text" button drawn by us, so it looks right
// on the dark background (the standard toolbar always draws black text).
type toolButton struct {
	icon    string
	text    string
	tip     string
	onClick func()
	enabled bool
	pressed bool
	w       *walk.CustomWidget
}

func newToolButton(icon, text, tip string, onClick func()) *toolButton {
	return &toolButton{icon: icon, text: text, tip: tip, onClick: onClick, enabled: true}
}

func (b *toolButton) decl() Widget {
	return CustomWidget{
		AssignTo:    &b.w,
		MinSize:     Size{Width: 78, Height: 70},
		MaxSize:     Size{Width: 96, Height: 70},
		PaintMode:   PaintBuffered,
		PaintPixels: b.paint,
		ToolTipText: b.tip,
		OnMouseDown: func(x, y int, button walk.MouseButton) {
			if button == walk.LeftButton && b.enabled {
				b.pressed = true
				b.w.Invalidate()
			}
		},
		OnMouseUp: func(x, y int, button walk.MouseButton) {
			if !b.pressed {
				return
			}
			b.pressed = false
			b.w.Invalidate()
			r := b.w.ClientBoundsPixels()
			if x >= 0 && y >= 0 && x < r.Width && y < r.Height && b.enabled && b.onClick != nil {
				b.onClick()
			}
		},
	}
}

func (b *toolButton) SetEnabled(v bool) {
	if b.enabled == v {
		return
	}
	b.enabled = v
	if b.w != nil {
		b.w.Invalidate()
	}
}

func (b *toolButton) paint(c *walk.Canvas, _ walk.Rectangle) error {
	r := b.w.ClientBoundsPixels()
	bg := brushBg
	if b.pressed {
		bg = brushPressed
	}
	if bg != nil {
		c.FillRectanglePixels(bg, r)
	}
	dpi := b.w.DPI()
	sc := func(v int) int { return v * dpi / 96 }
	isz := sc(34)
	name := b.icon
	if !b.enabled {
		name += "_dis"
	}
	if bmp := bitmap(name); bmp != nil {
		c.DrawImageStretchedPixels(bmp, walk.Rectangle{X: r.X + (r.Width-isz)/2, Y: r.Y + sc(5), Width: isz, Height: isz})
	}
	col := colText
	if !b.enabled {
		col = colDim
	}
	if tbFont != nil {
		c.DrawTextPixels(T(b.text), tbFont, col,
			walk.Rectangle{X: r.X + 2, Y: r.Y + sc(5) + isz + sc(3), Width: r.Width - 4, Height: sc(24)},
			walk.TextCenter|walk.TextSingleLine|walk.TextEndEllipsis|walk.TextNoPrefix)
	}
	return nil
}
