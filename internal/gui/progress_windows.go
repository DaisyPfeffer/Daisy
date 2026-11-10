//go:build windows

package gui

import (
	"fmt"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"

	"godm/internal/engine"
)

// progressWin is the per-download status window with the connection list.
type progressWin struct {
	a      *App
	id     string
	dlg    *walk.Dialog
	closed bool
	info   engine.Info

	urlLbl, status, size, done, rate, left, resume *walk.Label
	bar                                            *walk.ProgressBar
	seg                                            *walk.CustomWidget
	conns                                          *walk.TableView
	cm                                             *connModel
	pauseBtn                                       *walk.PushButton
	segPen                                         *walk.CosmeticPen
}

func (a *App) showProgress(id string) {
	if p := a.progress[id]; p != nil && !p.closed {
		p.dlg.Show()
		win.SetForegroundWindow(p.dlg.Handle())
		return
	}
	info, err := a.m.Get(id)
	if err != nil {
		return
	}
	if info.Status == engine.StatusCompleted {
		a.completeDialog(info)
		return
	}
	p := &progressWin{a: a, id: id, cm: &connModel{}}
	p.segPen, _ = walk.NewCosmeticPen(walk.PenSolid, walk.RGB(150, 150, 150))
	row := func(title string, lbl **walk.Label) []Widget {
		return []Widget{
			Label{Text: title},
			Label{AssignTo: lbl, EllipsisMode: EllipsisEnd, MinSize: Size{Width: 380}},
		}
	}
	var grid []Widget
	grid = append(grid, row(T("URL:"), &p.urlLbl)...)
	grid = append(grid, row(T("Status:"), &p.status)...)
	grid = append(grid, row(T("File size:"), &p.size)...)
	grid = append(grid, row(T("Downloaded:"), &p.done)...)
	grid = append(grid, row(T("Transfer rate:"), &p.rate)...)
	grid = append(grid, row(T("Time left:"), &p.left)...)
	grid = append(grid, row(T("Resume capability:"), &p.resume)...)

	err = Dialog{
		AssignTo: &p.dlg,
		Title:    DisplayName(info),
		Icon:     appIcon(),
		MinSize:  Size{Width: 580, Height: 470},
		Layout:   VBox{},
		Children: []Widget{
			Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: grid},
			ProgressBar{AssignTo: &p.bar, MinValue: 0, MaxValue: 1000},
			Label{Text: T("Start positions and download progress by connections")},
			CustomWidget{
				AssignTo:            &p.seg,
				MinSize:             Size{Height: 16},
				MaxSize:             Size{Height: 16},
				PaintMode:           PaintBuffered,
				InvalidatesOnResize: true,
				PaintPixels:         p.paintSegments,
			},
			TableView{
				AssignTo:                 &p.conns,
				Model:                    p.cm,
				NotSortableByHeaderClick: true,
				LastColumnStretched:      true,
				MinSize:                  Size{Height: 130},
				Columns: []TableViewColumn{
					{Title: "N.", Width: 40},
					{Title: T("Downloaded"), Width: 120},
					{Title: T("Info"), Width: 260},
				},
			},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				HSpacer{},
				PushButton{AssignTo: &p.pauseBtn, Text: T("Pause"), OnClicked: p.togglePause},
				PushButton{Text: T("Cancel"), ToolTipText: T("Stop this download and close the window"), OnClicked: func() {
					a.m.Pause(id)
					p.close()
				}},
				PushButton{Text: T("Hide"), ToolTipText: T("Close this window; the download keeps going"), OnClicked: p.close},
			}},
		},
	}.Create(a.mw)
	if err != nil {
		return
	}
	p.dlg.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		p.closed = true
		if a.progress[id] == p {
			delete(a.progress, id)
		}
	})
	a.progress[id] = p
	p.update(info)
	p.dlg.Show()
}

func setText(l *walk.Label, s string) {
	if l != nil && l.Text() != s {
		l.SetText(s)
	}
}

func (p *progressWin) update(i engine.Info) {
	if p.closed || p.dlg == nil || p.dlg.IsDisposed() {
		return
	}
	p.info = i
	pct := Percent(i)
	title := fmt.Sprintf("%.0f%% %s", pct, DisplayName(i))
	if p.dlg.Title() != title {
		p.dlg.SetTitle(title)
	}
	setText(p.urlLbl, i.URL)
	st := ""
	switch i.Status {
	case engine.StatusActive:
		st = T("Receiving data...")
		if i.Downloaded == 0 {
			st = T("Connecting...")
		}
	case engine.StatusQueued:
		st = T("Waiting in the queue")
	case engine.StatusPaused:
		st = T("Paused")
	case engine.StatusError:
		st = T("Error") + ": " + i.Error
		if !i.RetryAt.IsZero() {
			st += " (retrying at " + i.RetryAt.Format("15:04:05") + ")"
		}
	case engine.StatusCompleted:
		st = T("Complete")
	}
	setText(p.status, st)
	sz := SizeText(i)
	if sz == "" {
		sz = T("unknown")
	}
	setText(p.size, sz)
	d := HumanSize(i.Downloaded)
	if i.Size > 0 {
		d = fmt.Sprintf("%s  (%.2f %%)", d, pct)
	}
	setText(p.done, d)
	setText(p.rate, Localize(RateText(i)))
	setText(p.left, Localize(TimeLeftText(i)))
	res := T("Unknown")
	if i.Size >= 0 {
		res = T("No")
		if i.Resumable {
			res = T("Yes")
		}
	}
	setText(p.resume, res)
	p.bar.SetValue(int(pct * 10))
	p.seg.Invalidate()

	rows := ConnRows(i)
	same := len(rows) == len(p.cm.rows)
	p.cm.rows = rows
	if same && len(rows) > 0 {
		p.cm.PublishRowsChanged(0, len(rows)-1)
	} else if !same {
		p.cm.PublishRowsReset()
	}
	btn := T("Resume")
	if i.Status == engine.StatusActive || i.Status == engine.StatusQueued {
		btn = T("Pause")
	}
	if p.pauseBtn.Text() != btn {
		p.pauseBtn.SetText(btn)
	}
}

func (p *progressWin) paintSegments(c *walk.Canvas, _ walk.Rectangle) error {
	r := p.seg.ClientBoundsPixels()
	if brushTrack != nil {
		c.FillRectanglePixels(brushTrack, r)
	}
	i := p.info
	if i.Size > 0 && brushSeg != nil {
		w := float64(r.Width)
		for _, s := range i.Segments {
			pos := s.Pos
			if pos > s.End+1 {
				pos = s.End + 1
			}
			if pos <= s.Start {
				continue
			}
			x0 := r.X + int(float64(s.Start)/float64(i.Size)*w)
			x1 := r.X + int(float64(pos)/float64(i.Size)*w)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			c.FillRectanglePixels(brushSeg, walk.Rectangle{X: x0, Y: r.Y, Width: x1 - x0, Height: r.Height})
		}
	} else if i.Status == engine.StatusCompleted && brushSegDone != nil {
		c.FillRectanglePixels(brushSegDone, r)
	}
	if p.segPen != nil {
		c.DrawRectanglePixels(p.segPen, r)
	}
	return nil
}

func (p *progressWin) togglePause() {
	i, err := p.a.m.Get(p.id)
	if err != nil {
		return
	}
	if i.Status == engine.StatusActive || i.Status == engine.StatusQueued {
		p.a.m.Pause(p.id)
	} else {
		p.a.m.Resume(p.id)
	}
	p.a.refresh()
}

func (p *progressWin) close() {
	if p.closed {
		return
	}
	p.closed = true
	if p.a.progress[p.id] == p {
		delete(p.a.progress, p.id)
	}
	if p.dlg != nil && !p.dlg.IsDisposed() {
		p.dlg.Close(walk.DlgCmdCancel)
	}
}
