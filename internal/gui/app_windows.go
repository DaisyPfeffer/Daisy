//go:build windows

package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"

	"godm/internal/config"
	"godm/internal/engine"
	"godm/internal/manager"
	"godm/internal/server"
)

const AppName = "GoDM"
const Version = "2.0.0"

type App struct {
	cfg     *config.Config
	home    string
	log     *log.Logger
	m       *manager.Manager
	srv     *server.Server
	httpSrv *http.Server
	apiErr  error

	mw       *walk.MainWindow
	table    *walk.TableView
	tree     *walk.TreeView
	catPanel *walk.Composite

	toolbarBox *walk.Composite
	split      *walk.Splitter
	catHeader  *walk.Composite
	catLbl     *walk.Label
	closeLbl   *walk.Label
	menuKey    map[*walk.Action]string
	themeActs  map[string]*walk.Action
	langActs   []*walk.Action
	langCodes  []string
	trayActs   map[*walk.Action]string
	sbLeft   *walk.StatusBarItem
	sbRight  *walk.StatusBarItem
	ni       *walk.NotifyIcon
	model    *dlModel
	cats     *catModel
	cat      Category

	btn      map[string]*toolButton
	selSet   map[int]bool
	progress map[string]*progressWin

	prevStatus map[string]engine.Status
	primed     bool
	lastClip   string
	modal      int
	quitting   bool
	tick       int
	sched      schedule
	stop       chan struct{}
}

// Run starts the desktop app. args may contain "-tray" to start hidden.
func Run(args []string) error {
	home := config.Home()
	cfg, err := config.Load(home)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port))
	switch showExisting(addr, cfg.Token) {
	case existingWindow:
		return nil // already running: its window was brought to the front
	case existingServer:
		walk.MsgBox(nil, AppName, "The GoDM command-line server (godm-cli serve) is running and using the same downloads.\n\nClose it first, then start GoDM again.", walk.MsgBoxIconWarning)
		return nil
	}

	var logOut io.Writer = io.Discard
	if f, err := os.OpenFile(filepath.Join(home, "godm.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		defer f.Close()
		logOut = f
	}
	logger := log.New(logOut, "", log.LstdFlags)

	m, err := manager.New(cfg, home, logger)
	if err != nil {
		return err
	}
	a := &App{
		cfg: cfg, home: home, log: logger, m: m,
		model: &dlModel{}, cats: newCatModel(), cat: CatAll,
		menuKey: map[*walk.Action]string{}, themeActs: map[string]*walk.Action{}, trayActs: map[*walk.Action]string{},
		btn: map[string]*toolButton{}, selSet: map[int]bool{}, progress: map[string]*progressWin{},
		prevStatus: map[string]engine.Status{}, stop: make(chan struct{}),
	}
	a.sched.load(cfg)

	LoadLanguageDir(filepath.Join(filepath.Dir(exePath()), "lang"))
	LoadLanguageDir(langDir(home))
	SetLanguage(cfg.Language)
	enableDarkApp(themeDark(cfg.Theme))
	initTheme(cfg.Theme)
	hidden := false
	for _, s := range args {
		if s == "-tray" || s == "/tray" {
			hidden = true
		}
	}
	if err := a.buildMainWindow(!hidden); err != nil {
		m.Close()
		return err
	}
	a.startAPI(addr)
	a.setupTray()
	a.setupClipboard()
	a.refresh()
	go a.tickLoop()

	a.mw.Run()

	close(a.stop)
	if a.ni != nil {
		a.ni.Dispose()
	}
	if a.httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		a.httpSrv.Shutdown(ctx)
		cancel()
	}
	return m.Close()
}

const (
	existingNone = iota
	existingWindow
	existingServer
)

// showExisting asks an already running GoDM to show its window.
func showExisting(addr, token string) int {
	req, _ := http.NewRequest("POST", "http://"+addr+"/api/show", nil)
	req.Header.Set("X-GoDM-Token", token)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return existingNone
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return existingNone
	}
	var r struct{ Window bool }
	json.NewDecoder(resp.Body).Decode(&r)
	if r.Window {
		return existingWindow
	}
	return existingServer
}

func (a *App) startAPI(addr string) {
	a.srv = server.New(a.m, a.cfg.Token)
	a.srv.OnShow = func() { a.mw.Synchronize(a.showWindow) }
	a.srv.OnBrowserAdd = func(info engine.Info) {
		a.mw.Synchronize(func() {
			if a.cfg.ShowBrowserDialog {
				a.showWindow()
				a.fileInfoDialog(info.ID, true)
			} else {
				a.m.Resume(info.ID)
			}
		})
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		a.apiErr = err
		a.log.Printf("browser integration disabled: %v", err)
		return
	}
	a.httpSrv = &http.Server{Handler: a.srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go a.httpSrv.Serve(ln)
}

func (a *App) tickLoop() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
			a.mw.Synchronize(a.refresh)
		}
	}
}

// ---------------------------------------------------------------- window

func (a *App) buildMainWindow(visible bool) error {
	b := func(key, icon, text, tip string, fn func()) Widget {
		tb := newToolButton(icon, text, tip, fn)
		a.btn[key] = tb
		return tb.decl()
	}
	toolbar := []Widget{
		b("add", "tb_add", "Add URL", "Add a new download (Ctrl+N)", func() { a.addURLDialog("") }),
		b("resume", "tb_resume", "Resume", "Resume the selected downloads", a.resumeSelected),
		b("stop", "tb_stop", "Stop", "Stop the selected downloads", a.stopSelected),
		b("stopall", "tb_stopall", "Stop All", "Stop all downloads", a.m.PauseAll),
		b("delete", "tb_delete", "Delete", "Delete the selected downloads (Del)", a.deleteSelected),
		b("delcomp", "tb_delcomp", "Delete Co...", "Remove all finished downloads from the list", a.deleteCompleted),
		b("options", "tb_options", "Options", "Settings", a.optionsDialog),
		b("scheduler", "tb_scheduler", "Scheduler", "Start or stop the queue at set times", a.schedulerDialog),
		b("startq", "tb_startq", "Start Qu...", "Start all waiting and stopped downloads", a.m.ResumeAll),
		b("stopq", "tb_stopq", "Stop Qu...", "Stop the download queue", a.m.PauseAll),
		b("about", "tb_about", "About", "About GoDM", a.aboutDialog),
		HSpacer{},
	}

	cols := []TableViewColumn{
		{Title: Columns[0], Width: 250},
		{Title: Columns[1], Width: 28, Alignment: AlignCenter},
		{Title: Columns[2], Width: 85, Alignment: AlignFar},
		{Title: Columns[3], Width: 95},
		{Title: Columns[4], Width: 95},
		{Title: Columns[5], Width: 105},
		{Title: Columns[6], Width: 105},
		{Title: Columns[7], Width: 320},
	}

	var showCats, themeDarkAct, themeLightAct, themeSysAct *walk.Action
	langs := Languages()
	a.langActs = make([]*walk.Action, len(langs))
	a.langCodes = make([]string, len(langs))
	var langItems []MenuItem
	for i, l := range langs {
		i, code := i, l.Code
		a.langCodes[i] = code
		langItems = append(langItems, Action{AssignTo: &a.langActs[i], Text: l.Name, Checkable: true,
			OnTriggered: func() { a.setLanguage(code) }})
	}
	langItems = append(langItems, Separator{}, Action{Text: "More languages...", OnTriggered: a.openLanguageFolder})
	err := MainWindow{
		AssignTo:   &a.mw,
		Title:      AppName + " — Download Manager",
		Icon:       appIcon(),
		Size:       Size{Width: 1150, Height: 680},
		MinSize:    Size{Width: 700, Height: 400},
		Visible:    visible,
		Background: SolidColorBrush{Color: colBg},
		Layout:     VBox{MarginsZero: true, SpacingZero: true},
		MenuItems: []MenuItem{
			Menu{Text: "&Tasks", Items: []MenuItem{
				Action{Text: "&Add new download...\tCtrl+N", Shortcut: Shortcut{Modifiers: walk.ModControl, Key: walk.KeyN}, OnTriggered: func() { a.addURLDialog("") }},
				Action{Text: "Add &batch download from clipboard", OnTriggered: a.batchFromClipboard},
				Separator{},
				Action{Text: "E&xit", OnTriggered: a.quit},
			}},
			Menu{Text: "&File", Items: []MenuItem{
				Action{Text: "&Open", OnTriggered: a.openSelected},
				Action{Text: "Open &folder", OnTriggered: a.openSelectedFolder},
				Action{Text: "&Properties / progress", OnTriggered: a.showSelectedProgress},
				Separator{},
				Action{Text: "&Delete\tDel", OnTriggered: a.deleteSelected},
			}},
			Menu{Text: "&Downloads", Items: []MenuItem{
				Action{Text: "&Resume", OnTriggered: a.resumeSelected},
				Action{Text: "&Stop", OnTriggered: a.stopSelected},
				Action{Text: "Stop &all", OnTriggered: a.m.PauseAll},
				Action{Text: "Start &queue", OnTriggered: a.m.ResumeAll},
				Separator{},
				Action{Text: "Delete all &completed", OnTriggered: a.deleteCompleted},
				Separator{},
				Menu{Text: "Speed &limiter", Items: []MenuItem{
					Action{Text: "Off (full speed)", OnTriggered: func() { a.setLimit(0) }},
					Action{Text: "128 KB/sec", OnTriggered: func() { a.setLimit(128) }},
					Action{Text: "512 KB/sec", OnTriggered: func() { a.setLimit(512) }},
					Action{Text: "1 MB/sec", OnTriggered: func() { a.setLimit(1024) }},
					Action{Text: "5 MB/sec", OnTriggered: func() { a.setLimit(5 * 1024) }},
					Action{Text: "Custom...", OnTriggered: a.optionsDialog},
				}},
				Action{Text: "S&cheduler...", OnTriggered: a.schedulerDialog},
				Action{Text: "&Options...", OnTriggered: a.optionsDialog},
			}},
			Menu{Text: "&View", Items: []MenuItem{
				Action{AssignTo: &showCats, Text: "&Categories", Checkable: true, Checked: true, OnTriggered: func() {
					a.catPanel.SetVisible(showCats.Checked())
				}},
				Separator{},
				Action{Text: "All downloads", OnTriggered: func() { a.selectCategory(CatAll) }},
				Action{Text: "Unfinished", OnTriggered: func() { a.selectCategory(CatUnfinished) }},
				Action{Text: "Finished", OnTriggered: func() { a.selectCategory(CatFinished) }},
				Separator{},
				Menu{Text: "&Theme", Items: []MenuItem{
					Action{AssignTo: &themeDarkAct, Text: "Dark", Checkable: true, OnTriggered: func() { a.setTheme("dark") }},
					Action{AssignTo: &themeLightAct, Text: "Light", Checkable: true, OnTriggered: func() { a.setTheme("light") }},
					Action{AssignTo: &themeSysAct, Text: "Same as Windows", Checkable: true, OnTriggered: func() { a.setTheme("system") }},
				}},
				Menu{Text: "&Language", Items: langItems},
			}},
			Menu{Text: "&Help", Items: []MenuItem{
				Action{Text: "&Browser integration...", OnTriggered: a.browserHelpDialog},
				Action{Text: "Open &log file", OnTriggered: func() { shellOpen(filepath.Join(a.home, "godm.log")) }},
				Separator{},
				Action{Text: "&About GoDM", OnTriggered: a.aboutDialog},
			}},
		},
		Children: []Widget{
			Composite{
				AssignTo:   &a.toolbarBox,
				MaxSize:    Size{Height: 84},
				Background: SolidColorBrush{Color: colBg},
				Layout:     HBox{Margins: Margins{Left: 6, Top: 4, Right: 6, Bottom: 4}, Spacing: 0},
				Children:   toolbar,
			},
			HSplitter{
				AssignTo:      &a.split,
				StretchFactor: 100,
				HandleWidth:   5,
				Background:    SolidColorBrush{Color: colBg},
				Children: []Widget{
					Composite{
						AssignTo:      &a.catPanel,
						StretchFactor: 1,
						MinSize:       Size{Width: 150},
						Background:    SolidColorBrush{Color: colPanel},
						Layout:        VBox{MarginsZero: true, SpacingZero: true},
						Children: []Widget{
							Composite{
								AssignTo:   &a.catHeader,
								Background: SolidColorBrush{Color: colBg},
								Layout:     HBox{Margins: Margins{Left: 6, Top: 3, Right: 4, Bottom: 3}},
								Children: []Widget{
									Label{AssignTo: &a.catLbl, Text: "Categories", TextColor: colText},
									HSpacer{},
									Label{AssignTo: &a.closeLbl, Text: "✕", TextColor: colDim, ToolTipText: "Hide categories",
										OnMouseUp: func(x, y int, b walk.MouseButton) {
											a.catPanel.SetVisible(false)
											showCats.SetChecked(false)
										}},
								},
							},
							TreeView{
								AssignTo:             &a.tree,
								Model:                a.cats,
								ItemHeight:           20,
								OnCurrentItemChanged: a.onCategoryChanged,
							},
						},
					},
					TableView{
						AssignTo:                 &a.table,
						StretchFactor:            6,
						Model:                    a.model,
						Columns:                  cols,
						MultiSelection:           true,
						NotSortableByHeaderClick: true,
						LastColumnStretched:      true,
						CustomRowHeight:          22,
						StyleCell:                a.styleCell,
						OnItemActivated:          a.onActivate,
						OnSelectedIndexesChanged: func() { a.syncSelection(); a.updateButtons() },
						OnKeyDown: func(key walk.Key) {
							switch key {
							case walk.KeyDelete:
								a.deleteSelected()
							case walk.KeyReturn:
								a.onActivate()
							}
						},
						ContextMenuItems: []MenuItem{
							Action{Text: "Open", OnTriggered: a.openSelected},
							Action{Text: "Open folder", OnTriggered: a.openSelectedFolder},
							Separator{},
							Action{Text: "Resume", OnTriggered: a.resumeSelected},
							Action{Text: "Stop", OnTriggered: a.stopSelected},
							Action{Text: "Show progress", OnTriggered: a.showSelectedProgress},
							Separator{},
							Action{Text: "Copy address", OnTriggered: a.copySelectedURL},
							Action{Text: "Delete", OnTriggered: a.deleteSelected},
						},
					},
				},
			},
		},
		StatusBarItems: []StatusBarItem{
			{AssignTo: &a.sbLeft, Width: 420},
			{AssignTo: &a.sbRight, Width: 380},
		},
	}.Create()
	if err != nil {
		return err
	}

	a.themeActs["dark"], a.themeActs["light"], a.themeActs["system"] = themeDarkAct, themeLightAct, themeSysAct
	for _, r := range a.cats.roots {
		a.tree.SetExpanded(r, r.cat == CatAll)
	}
	a.tree.SetCurrentItem(a.cats.roots[0])

	toldTray := false
	a.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if a.quitting {
			return
		}
		if a.cfg.CloseToTray && a.ni != nil {
			*canceled = true
			a.mw.Hide()
			if !toldTray {
				toldTray = true
				a.ni.ShowInfo(T("GoDM is still running"), T("Downloads continue in the background. Right-click the tray icon to exit."))
			}
			return
		}
		a.quitting = true
		for _, p := range a.progress {
			p.close()
		}
	})
	a.mw.SetIcon(appIcon())
	a.applyTheme()
	a.applyLanguage()
	a.updateButtons()
	return nil
}

func (a *App) styleCell(style *walk.CellStyle) {
	row := style.Row()
	normal := !a.selSet[row]
	if normal {
		style.BackgroundColor = colPanel
		if row%2 == 1 {
			style.BackgroundColor = colRowAlt
		}
		style.TextColor = colText
	} else {
		style.BackgroundColor = colSel
		style.TextColor = walk.RGB(255, 255, 255)
	}
	if row < 0 || row >= len(a.model.rows) {
		return
	}
	r := a.model.rows[row]
	switch style.Col() {
	case 0:
		if b := bitmap(r.Icon); b != nil {
			style.Image = b
		}
	case 3:
		if normal {
			switch r.Info.Status {
			case engine.StatusCompleted:
				style.TextColor = colDone
			case engine.StatusError:
				style.TextColor = colErr
			case engine.StatusActive:
				style.TextColor = colAccent
			}
		}
	}
}

func (a *App) onCategoryChanged() {
	if it, ok := a.tree.CurrentItem().(*catItem); ok && it != nil {
		a.cat = it.cat
		a.refresh()
	}
}

func (a *App) selectCategory(c Category) {
	var find func(items []*catItem) *catItem
	find = func(items []*catItem) *catItem {
		for _, it := range items {
			if it.cat == c {
				return it
			}
			if f := find(it.children); f != nil {
				return f
			}
		}
		return nil
	}
	if it := find(a.cats.roots); it != nil {
		a.tree.SetCurrentItem(it)
	}
}

// ---------------------------------------------------------------- refresh

func (a *App) refresh() {
	if a.mw == nil || a.mw.IsDisposed() {
		return
	}
	now := time.Now()
	list := a.m.List()
	rows := BuildRows(list, a.cat, now)

	sel := a.selectedIDs()
	same := SameIDs(rows, a.model.rows)
	a.model.rows = rows
	if same {
		if len(rows) > 0 {
			a.model.PublishRowsChanged(0, len(rows)-1)
		}
	} else {
		a.model.PublishRowsReset()
		var idx []int
		for i, r := range rows {
			for _, id := range sel {
				if r.ID == id {
					idx = append(idx, i)
				}
			}
		}
		a.table.SetSelectedIndexes(idx)
		a.syncSelection()
	}

	active, speed, queued := Summary(list)
	left := fmt.Sprintf("  %d %s  ·  %d %s  ·  %d %s", len(list), T("downloads"), active, T("active"), queued, T("waiting"))
	if lim := a.cfg.SpeedLimit; lim > 0 {
		left += "  ·  " + T("limit") + " " + Localize(SpeedText(lim))
	}
	a.sbLeft.SetText(left)
	right := "  " + Localize(SpeedText(speed))
	if a.apiErr != nil {
		right += "  ·  " + T("browser integration off (port in use)")
	} else if s := a.sched.describe(); s != "" {
		right += "  ·  " + s
	}
	a.sbRight.SetText(right)
	if a.ni != nil {
		tip := AppName
		if active > 0 {
			tip = fmt.Sprintf("%s — %d active, %s", AppName, active, SpeedText(speed))
		}
		a.ni.SetToolTip(tip)
	}

	byID := make(map[string]engine.Info, len(list))
	for _, i := range list {
		byID[i.ID] = i
		prev, seen := a.prevStatus[i.ID]
		if a.primed && seen && prev != engine.StatusCompleted && i.Status == engine.StatusCompleted {
			a.onCompleted(i)
		}
		a.prevStatus[i.ID] = i.Status
	}
	a.primed = true
	for id, p := range a.progress {
		if i, ok := byID[id]; ok {
			p.update(i)
		} else {
			p.close()
		}
	}
	a.sched.check(a, list, now)
	a.updateButtons()
	a.tick++
	if a.tick%10 == 0 {
		keepTableStyled(a.table, themeDark(a.cfg.Theme))
	}
}

func (a *App) updateButtons() {
	if len(a.btn) == 0 || a.table == nil {
		return
	}
	canResume, canStop := false, false
	sel := a.selectedInfos()
	for _, i := range sel {
		switch i.Status {
		case engine.StatusPaused, engine.StatusError:
			canResume = true
		case engine.StatusActive, engine.StatusQueued:
			canStop = true
		}
	}
	anyActive, anyDone := false, false
	for _, r := range a.m.List() {
		if r.Status == engine.StatusActive || r.Status == engine.StatusQueued {
			anyActive = true
		}
		if r.Status == engine.StatusCompleted {
			anyDone = true
		}
	}
	a.btn["resume"].SetEnabled(canResume)
	a.btn["stop"].SetEnabled(canStop)
	a.btn["stopall"].SetEnabled(anyActive)
	a.btn["delete"].SetEnabled(len(sel) > 0)
	a.btn["delcomp"].SetEnabled(anyDone)
	a.btn["stopq"].SetEnabled(anyActive)
}

// syncSelection remembers which rows are selected so cells can be colored
// without depending on the current Windows theme colors.
func (a *App) syncSelection() {
	a.selSet = map[int]bool{}
	for _, i := range a.table.SelectedIndexes() {
		a.selSet[i] = true
	}
}

func (a *App) selectedIDs() []string {
	var ids []string
	for _, i := range a.table.SelectedIndexes() {
		if i >= 0 && i < len(a.model.rows) {
			ids = append(ids, a.model.rows[i].ID)
		}
	}
	return ids
}

func (a *App) selectedInfos() []engine.Info {
	var out []engine.Info
	for _, id := range a.selectedIDs() {
		if i, err := a.m.Get(id); err == nil {
			out = append(out, i)
		}
	}
	return out
}

// ---------------------------------------------------------------- actions

func (a *App) onActivate() {
	sel := a.selectedInfos()
	if len(sel) != 1 {
		return
	}
	if sel[0].Status == engine.StatusCompleted {
		a.openSelected()
	} else {
		a.showProgress(sel[0].ID)
	}
}

func (a *App) resumeSelected() {
	for _, id := range a.selectedIDs() {
		a.m.Resume(id)
	}
	a.refresh()
}

func (a *App) stopSelected() {
	for _, id := range a.selectedIDs() {
		a.m.Pause(id)
	}
	a.refresh()
}

func (a *App) deleteSelected() {
	sel := a.selectedInfos()
	if len(sel) == 0 {
		return
	}
	hasFiles := false
	for _, i := range sel {
		if i.Status == engine.StatusCompleted {
			hasFiles = true
		}
	}
	what := "the selected download"
	if len(sel) > 1 {
		what = fmt.Sprintf("the %d selected downloads", len(sel))
	}
	deleteFiles := false
	if hasFiles {
		switch walk.MsgBox(a.mw, "Delete", "Delete "+what+"?\n\nYes: remove from the list AND delete the downloaded files from disk.\nNo: remove from the list only (files stay).",
			walk.MsgBoxYesNoCancel|walk.MsgBoxIconQuestion) {
		case walk.DlgCmdYes:
			deleteFiles = true
		case walk.DlgCmdNo:
		default:
			return
		}
	} else if walk.MsgBox(a.mw, "Delete", "Delete "+what+"?\nPartly downloaded data will be removed.",
		walk.MsgBoxOKCancel|walk.MsgBoxIconQuestion) != walk.DlgCmdOK {
		return
	}
	for _, i := range sel {
		a.m.Remove(i.ID, deleteFiles)
		if p := a.progress[i.ID]; p != nil {
			p.close()
		}
	}
	a.refresh()
}

func (a *App) deleteCompleted() {
	if walk.MsgBox(a.mw, T("Delete completed"), T("Remove all finished downloads from the list?\nThe files stay on your disk."),
		walk.MsgBoxOKCancel|walk.MsgBoxIconQuestion) == walk.DlgCmdOK {
		a.m.RemoveCompleted()
		a.refresh()
	}
}

func (a *App) openSelected() {
	for _, i := range a.selectedInfos() {
		if i.Status == engine.StatusCompleted && i.Path != "" {
			shellOpen(i.Path)
		}
	}
}

func (a *App) openSelectedFolder() {
	sel := a.selectedInfos()
	if len(sel) == 0 {
		shellOpen(a.cfg.DownloadDir)
		return
	}
	openFolder(sel[0])
}

func (a *App) showSelectedProgress() {
	for _, i := range a.selectedInfos() {
		a.showProgress(i.ID)
	}
}

func (a *App) copySelectedURL() {
	var urls []string
	for _, i := range a.selectedInfos() {
		urls = append(urls, i.URL)
	}
	if len(urls) > 0 {
		a.lastClip = urls[len(urls)-1] // do not "catch" our own copy
		walk.Clipboard().SetText(strings.Join(urls, "\r\n"))
	}
}

func (a *App) batchFromClipboard() {
	text, _ := walk.Clipboard().Text()
	urls := ExtractURLs(text)
	if len(urls) == 0 {
		walk.MsgBox(a.mw, T("Batch download"), T("No http/https links were found on the clipboard."), walk.MsgBoxIconInformation)
		return
	}
	if walk.MsgBox(a.mw, "Batch download", fmt.Sprintf("Add %d links from the clipboard to the queue?", len(urls)),
		walk.MsgBoxOKCancel|walk.MsgBoxIconQuestion) != walk.DlgCmdOK {
		return
	}
	for _, u := range urls {
		if _, err := a.m.Add(engine.Request{URL: u}, manager.AddOptions{}); err != nil {
			a.log.Printf("batch add %s: %v", u, err)
		}
	}
	a.refresh()
}

func (a *App) setLimit(kb int64) {
	v := kb * 1024
	a.m.UpdateSettings(manager.Settings{SpeedLimit: &v})
	a.refresh()
}

func (a *App) onCompleted(i engine.Info) {
	if p := a.progress[i.ID]; p != nil {
		p.close()
	}
	if a.cfg.ShowCompleteDialog && a.mw.Visible() {
		a.completeDialog(i)
	} else if a.ni != nil {
		a.ni.ShowInfo(T("Download complete"), DisplayName(i))
	}
}

func (a *App) showWindow() {
	if a.mw.IsDisposed() {
		return
	}
	a.mw.Show()
	if win.IsIconic(a.mw.Handle()) {
		win.ShowWindow(a.mw.Handle(), win.SW_RESTORE)
	}
	win.SetForegroundWindow(a.mw.Handle())
}

func (a *App) quit() {
	active := 0
	for _, i := range a.m.List() {
		if i.Status == engine.StatusActive {
			active++
		}
	}
	if active > 0 && walk.MsgBox(a.mw, "Exit GoDM",
		fmt.Sprintf("%d downloads are running. They will be paused and continue next time you start GoDM.\n\nExit now?", active),
		walk.MsgBoxOKCancel|walk.MsgBoxIconQuestion) != walk.DlgCmdOK {
		return
	}
	a.quitting = true
	for _, p := range a.progress {
		p.close()
	}
	a.mw.Close()
}

// ---------------------------------------------------------------- tray & clipboard

func (a *App) setupTray() {
	ni, err := walk.NewNotifyIcon(a.mw)
	if err != nil {
		a.log.Printf("tray icon: %v", err)
		return
	}
	a.ni = ni
	if ic := appIcon(); ic != nil {
		ni.SetIcon(ic)
	}
	ni.SetToolTip(AppName)
	ni.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			a.showWindow()
		}
	})
	ni.MessageClicked().Attach(a.showWindow)
	add := func(text string, fn func()) {
		act := walk.NewAction()
		a.trayActs[act] = text
		act.SetText(T(text))
		act.Triggered().Attach(fn)
		ni.ContextMenu().Actions().Add(act)
	}
	add("Open GoDM", a.showWindow)
	add("Add new download...", func() { a.showWindow(); a.addURLDialog("") })
	add("Start queue", a.m.ResumeAll)
	add("Stop all downloads", a.m.PauseAll)
	ni.ContextMenu().Actions().Add(walk.NewSeparatorAction())
	add("Exit", func() { a.showWindow(); a.quit() })
	ni.SetVisible(true)
}

func (a *App) setupClipboard() {
	walk.Clipboard().ContentsChanged().Attach(func() {
		if !a.cfg.MonitorClipboard || a.modal > 0 {
			return
		}
		text, err := walk.Clipboard().Text()
		if err != nil {
			return
		}
		u, ok := CatchableURL(text)
		if !ok || u == a.lastClip {
			return
		}
		a.lastClip = u
		a.showWindow()
		a.addURLDialog(u)
	})
}

// ---------------------------------------------------------------- helpers

func shellOpen(path string) {
	verb := syscall.StringToUTF16Ptr("open")
	win.ShellExecute(0, verb, syscall.StringToUTF16Ptr(path), nil, nil, win.SW_SHOWNORMAL)
}

func openFolder(i engine.Info) {
	p := i.Path
	if i.Status != engine.StatusCompleted && p != "" {
		p += engine.PartSuffix
	}
	if p != "" {
		if _, err := os.Stat(p); err == nil {
			cmd := exec.Command("explorer.exe")
			cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + p + `"`}
			cmd.Start()
			return
		}
		shellOpen(filepath.Dir(p))
		return
	}
	if i.Dir != "" {
		shellOpen(i.Dir)
	}
}

func openWith(path string) {
	cmd := exec.Command("rundll32.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `rundll32.exe shell32.dll,OpenAs_RunDLL ` + path}
	cmd.Start()
}

func exePath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

// ---------------------------------------------------------------- theme & language

func langDir(home string) string { return filepath.Join(home, "lang") }

// applyTheme repaints the whole window with the theme in the settings.
func (a *App) applyTheme() {
	if a.mw == nil || a.mw.IsDisposed() {
		return
	}
	dark := themeDark(a.cfg.Theme)
	setPalette(dark)
	enableDarkApp(dark)
	a.mw.SetBackground(brushBg)
	a.toolbarBox.SetBackground(brushBg)
	a.split.SetBackground(brushBg)
	a.catPanel.SetBackground(brushPanel)
	a.catHeader.SetBackground(brushBg)
	a.catLbl.SetTextColor(colText)
	a.closeLbl.SetTextColor(colDim)
	titleBar(a.mw.Handle(), dark)
	styleTree(a.tree, dark)
	styleTable(a.table, dark)
	for _, b := range a.btn {
		if b.w != nil {
			b.w.Invalidate()
		}
	}
	a.table.Invalidate()
	win.DrawMenuBar(a.mw.Handle())
	a.markChecks()
}

// applyLanguage translates the window to the language in the settings.
func (a *App) applyLanguage() {
	if a.mw == nil || a.mw.IsDisposed() {
		return
	}
	SetLanguage(a.cfg.Language)
	a.mw.SetTitle(AppName + " — " + T("Download Manager"))
	a.catLbl.SetText(T("Categories"))
	a.closeLbl.SetToolTipText(T("Hide categories"))
	for i, k := range Columns {
		a.table.Columns().At(i).SetTitle(T(k))
	}
	a.translateActions(a.mw.Menu().Actions())
	if cm := a.table.ContextMenu(); cm != nil {
		a.translateActions(cm.Actions())
	}
	for act, key := range a.trayActs {
		act.SetText(T(key))
	}
	for _, b := range a.btn {
		if b.w != nil {
			b.w.SetToolTipText(T(b.tip))
			b.w.Invalidate()
		}
	}
	for _, r := range a.cats.roots {
		a.cats.PublishItemChanged(r)
		for _, c := range r.children {
			a.cats.PublishItemChanged(c)
		}
	}
	a.table.Invalidate()
	a.refresh()
	win.DrawMenuBar(a.mw.Handle())
	a.markChecks()
}

// translateActions walks a menu tree and sets every item's text. The first time an item
// is seen its English text is remembered, so switching languages never loses it.
func (a *App) translateActions(list *walk.ActionList) {
	if list == nil {
		return
	}
	for i := 0; i < list.Len(); i++ {
		act := list.At(i)
		if act.IsSeparator() {
			continue
		}
		key, ok := a.menuKey[act]
		if !ok {
			key = act.Text()
			a.menuKey[act] = key
		}
		if a.langIsNative(act) {
			continue
		}
		act.SetText(T(key))
		if m := act.Menu(); m != nil {
			a.translateActions(m.Actions())
		}
	}
}

// langIsNative is true for the language list entries, which keep their own name.
func (a *App) langIsNative(act *walk.Action) bool {
	for _, l := range a.langActs {
		if l == act {
			return true
		}
	}
	return false
}

// markChecks puts the check marks next to the current theme and language.
func (a *App) markChecks() {
	cur := strings.ToLower(a.cfg.Theme)
	if cur == "auto" {
		cur = "system"
	}
	if cur != "light" && cur != "system" {
		cur = "dark"
	}
	for k, act := range a.themeActs {
		if act != nil {
			act.SetChecked(k == cur)
		}
	}
	for i, act := range a.langActs {
		if act != nil {
			act.SetChecked(a.langCodes[i] == CurrentLanguage())
		}
	}
}

func (a *App) setTheme(name string) {
	a.cfg.Update(func(c *config.Config) { c.Theme = name })
	a.applyTheme()
}

func (a *App) setLanguage(code string) {
	a.cfg.Update(func(c *config.Config) { c.Language = code })
	a.applyLanguage()
}

// openLanguageFolder opens the folder where extra language files go, with a template to copy.
func (a *App) openLanguageFolder() {
	dir := langDir(a.home)
	os.MkdirAll(dir, 0o755)
	ExportTemplate(filepath.Join(dir, "_template.json"))
	walk.MsgBox(a.mw, "GoDM", T("To add a language, copy _template.json to <code>.json (for example de.json), translate the texts on the right side, set \"name\" and restart GoDM. The folder opens now."), walk.MsgBoxIconInformation)
	shellOpen(dir)
}
