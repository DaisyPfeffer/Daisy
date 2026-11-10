//go:build windows

package gui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"golang.org/x/sys/windows/registry"

	"godm/internal/config"
	"godm/internal/engine"
	"godm/internal/manager"
	"godm/internal/nativehost"
)

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return 0
}

func (a *App) categoryDir(cat string) string {
	if a.cfg.Categorize {
		return filepath.Join(a.cfg.DownloadDir, cat)
	}
	return a.cfg.DownloadDir
}

// ---------------------------------------------------------------- Add URL

func (a *App) addURLDialog(prefill string) {
	if prefill == "" {
		if t, err := walk.Clipboard().Text(); err == nil {
			if u, ok := CatchableURL(t); ok {
				prefill = u
			} else if us := ExtractURLs(t); len(us) == 1 {
				prefill = us[0]
			}
		}
	}
	a.modal++
	defer func() { a.modal-- }()

	var (
		dlg              *walk.Dialog
		addr, user, pass *walk.LineEdit
		useAuth          *walk.CheckBox
		okBtn, cancelBtn *walk.PushButton
		gotURL           string
		gotUser, gotPass string
	)
	res, err := Dialog{
		AssignTo:      &dlg,
		Title:         T("Enter new address to download"),
		Icon:          appIcon(),
		DefaultButton: &okBtn,
		CancelButton:  &cancelBtn,
		MinSize:       Size{Width: 540},
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: T("Address:")},
			LineEdit{AssignTo: &addr, Text: prefill, CueBanner: "https://example.com/file.zip"},
			CheckBox{AssignTo: &useAuth, Text: T("Use authorization"), OnCheckStateChanged: func() {
				if user != nil && pass != nil {
					on := useAuth.Checked()
					user.SetEnabled(on)
					pass.SetEnabled(on)
				}
			}},
			Composite{
				Layout: Grid{Columns: 2, MarginsZero: true},
				Children: []Widget{
					Label{Text: T("Login:")}, LineEdit{AssignTo: &user, Enabled: false},
					Label{Text: T("Password:")}, LineEdit{AssignTo: &pass, PasswordMode: true, Enabled: false},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &okBtn, Text: T("OK"), OnClicked: func() {
						u := strings.TrimSpace(addr.Text())
						if u == "" {
							return
						}
						if !strings.Contains(u, "://") {
							u = "https://" + u
						}
						gotURL = u
						if useAuth.Checked() {
							gotUser, gotPass = user.Text(), pass.Text()
						}
						dlg.Accept()
					}},
					PushButton{AssignTo: &cancelBtn, Text: T("Cancel"), OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Run(a.mw)
	if err != nil || res != walk.DlgCmdOK || gotURL == "" {
		return
	}
	info, err := a.m.Add(engine.Request{URL: gotURL, Username: gotUser, Password: gotPass}, manager.AddOptions{Paused: true})
	if err != nil {
		walk.MsgBox(a.mw, "Add download", err.Error(), walk.MsgBoxIconWarning)
		return
	}
	a.refresh()
	a.fileInfoDialog(info.ID, false)
}

// ---------------------------------------------------------------- Download File Info

// fileInfoDialog asks where to save a download that was added paused, then
// starts it, leaves it for later, or removes it.
func (a *App) fileInfoDialog(id string, fromBrowser bool) {
	info, err := a.m.Get(id)
	if err != nil {
		return
	}
	a.modal++
	defer func() { a.modal-- }()

	name := DisplayName(info)
	cat := engine.Category(name)
	var (
		dlg                         *walk.Dialog
		saveAs, desc                *walk.LineEdit
		catCB, typeCB               *walk.ComboBox
		sizeLbl                     *walk.Label
		startBtn, laterBtn, cancelB *walk.PushButton
		choice                      int // 1 = start, 2 = later
		setting, userEdited, closed bool
		gotPath, gotDesc            string
		syncing                     bool
	)
	setPath := func(p string) {
		setting = true
		saveAs.SetText(p)
		setting = false
	}
	title := T("Download File Info")
	if fromBrowser {
		title = T("Download File Info") + " — " + T("from your browser")
	}
	err = Dialog{
		AssignTo:      &dlg,
		Title:         title,
		Icon:          appIcon(),
		DefaultButton: &startBtn,
		CancelButton:  &cancelB,
		MinSize:       Size{Width: 600},
		Layout:        Grid{Columns: 3},
		Children: []Widget{
			Label{Text: T("URL:")},
			LineEdit{Text: info.URL, ReadOnly: true, ColumnSpan: 2},
			Label{Text: T("Category:")},
			ComboBox{AssignTo: &catCB, Model: FileCategories, CurrentIndex: indexOf(FileCategories, cat), ColumnSpan: 2,
				OnCurrentIndexChanged: func() {
					if syncing || catCB == nil || saveAs == nil || catCB.CurrentIndex() < 0 {
						return
					}
					c := FileCategories[catCB.CurrentIndex()]
					setPath(filepath.Join(a.categoryDir(c), filepath.Base(saveAs.Text())))
				}},
			Label{Text: T("File type:")},
			ComboBox{AssignTo: &typeCB, Editable: true, Model: FileTypes, ColumnSpan: 2,
				ToolTipText: T("Change the file type (extension) the file is saved as")},
			Label{Text: T("Save As:")},
			LineEdit{AssignTo: &saveAs, Text: filepath.Join(a.categoryDir(cat), name), OnTextChanged: func() {
				if !setting {
					userEdited = true
				}
			}},
			PushButton{Text: "...", MaxSize: Size{Width: 34}, OnClicked: func() {
				fd := walk.FileDialog{Title: T("Save As"), FilePath: saveAs.Text(), InitialDirPath: filepath.Dir(saveAs.Text())}
				if ok, _ := fd.ShowSave(dlg); ok && fd.FilePath != "" {
					setPath(fd.FilePath)
					userEdited = true
				}
			}},
			Label{Text: T("Description:")},
			LineEdit{AssignTo: &desc, Text: info.Description, ColumnSpan: 2},
			Label{Text: T("File size:")},
			Label{AssignTo: &sizeLbl, Text: T("getting file information..."), ColumnSpan: 2},
			Composite{
				ColumnSpan: 3,
				Layout:     HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &startBtn, Text: T("Start Download"), OnClicked: func() {
						choice, gotPath, gotDesc = 1, saveAs.Text(), desc.Text()
						dlg.Accept()
					}},
					PushButton{AssignTo: &laterBtn, Text: T("Download Later"), OnClicked: func() {
						choice, gotPath, gotDesc = 2, saveAs.Text(), desc.Text()
						dlg.Accept()
					}},
					PushButton{AssignTo: &cancelB, Text: T("Cancel"), OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Create(a.mw)
	if err != nil {
		return
	}
	userEdited = false
	typeCB.SetText(strings.ToLower(filepath.Ext(name)))

	// Changing the type changes the extension in "Save As" and moves the file to the matching
	// category folder (only when the folder is still the automatic one).
	typeCB.TextChanged().Attach(func() {
		if syncing || saveAs == nil {
			return
		}
		ext := strings.TrimSpace(typeCB.Text())
		if ext != "" && !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		p := saveAs.Text()
		dir, file := filepath.Split(p)
		newFile := strings.TrimSuffix(file, filepath.Ext(file)) + ext
		oldCat := FileCategories[max(catCB.CurrentIndex(), 0)]
		newCat := engine.Category(newFile)
		if filepath.Clean(dir) == filepath.Clean(a.categoryDir(oldCat)) {
			dir = a.categoryDir(newCat) + string(filepath.Separator)
			syncing = true
			catCB.SetCurrentIndex(indexOf(FileCategories, newCat))
			syncing = false
		}
		userEdited = true
		setPath(filepath.Join(dir, newFile))
	})
	saveAs.TextChanged().Attach(func() {
		if syncing {
			return
		}
		syncing = true
		typeCB.SetText(strings.ToLower(filepath.Ext(saveAs.Text())))
		syncing = false
	})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		fi, perr := a.m.Engine().Probe(ctx, info.Request)
		a.mw.Synchronize(func() {
			if closed {
				return
			}
			if perr != nil {
				sizeLbl.SetText("unknown (" + shorten(perr.Error(), 70) + ")")
				return
			}
			txt := "unknown"
			if fi.Size >= 0 {
				txt = HumanSize(fi.Size)
			}
			if fi.Resumable {
				txt += "   ·   resume supported"
			} else {
				txt += "   ·   no resume support"
			}
			sizeLbl.SetText(txt)
			if !userEdited && fi.FileName != "" && fi.FileName != name {
				c := engine.Category(fi.FileName)
				setting = true
				catCB.SetCurrentIndex(indexOf(FileCategories, c))
				setting = false
				setPath(filepath.Join(a.categoryDir(c), fi.FileName))
			}
		})
	}()
	res := dlg.Run()
	closed = true
	cancel()

	if res != walk.DlgCmdOK || choice == 0 {
		a.m.Remove(id, false)
		a.refresh()
		return
	}
	gotPath = strings.TrimSpace(gotPath)
	dir, file := filepath.Split(gotPath)
	if file == "" {
		file = name
	}
	if dir == "" {
		dir = a.categoryDir(cat)
	}
	if err := a.m.Edit(id, func(r *engine.Request) {
		r.Dir = filepath.Clean(dir)
		r.FileName = file
		r.Description = gotDesc
	}); err != nil {
		walk.MsgBox(a.mw, "Download", err.Error(), walk.MsgBoxIconWarning)
	}
	if choice == 1 {
		a.m.Resume(id)
		a.showProgress(id)
	}
	a.refresh()
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// ---------------------------------------------------------------- Download complete

func (a *App) completeDialog(i engine.Info) {
	var (
		dlg      *walk.Dialog
		dontShow *walk.CheckBox
	)
	err := Dialog{
		AssignTo: &dlg,
		Title:    T("Download complete"),
		Icon:     appIcon(),
		MinSize:  Size{Width: 520},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: T("Download complete"), Font: Font{Family: "Segoe UI", PointSize: 12, Bold: true}},
			Composite{
				Layout: Grid{Columns: 2, MarginsZero: true},
				Children: []Widget{
					Label{Text: T("Downloaded:")}, Label{Text: HumanSize(i.Size)},
					Label{Text: T("Address:")}, LineEdit{Text: i.URL, ReadOnly: true},
					Label{Text: T("The file saved as:")}, LineEdit{Text: i.Path, ReadOnly: true},
				},
			},
			CheckBox{AssignTo: &dontShow, Text: T("Don't show this dialog again")},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{Text: T("Open"), OnClicked: func() { shellOpen(i.Path); dlg.Close(0) }},
					PushButton{Text: T("Open with..."), OnClicked: func() { openWith(i.Path); dlg.Close(0) }},
					PushButton{Text: T("Open folder"), OnClicked: func() { openFolder(i); dlg.Close(0) }},
					HSpacer{},
					PushButton{Text: T("Close"), OnClicked: func() { dlg.Close(0) }},
				},
			},
		},
	}.Create(a.mw)
	if err != nil {
		return
	}
	dlg.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if dontShow.Checked() {
			a.cfg.Update(func(c *config.Config) { c.ShowCompleteDialog = false })
		}
	})
	dlg.Show()
}

// ---------------------------------------------------------------- Options

func (a *App) optionsDialog() {
	a.modal++
	defer func() { a.modal-- }()
	c := a.cfg
	var (
		dlg                                               *walk.Dialog
		conns, maxc, limit                                *walk.NumberEdit
		dir                                               *walk.LineEdit
		retry, categorize, clip, complete, ask, tray, run *walk.CheckBox
		okBtn, cancelBtn                                  *walk.PushButton
		themeCB, langCB                                   *walk.ComboBox
	)
	themeKeys := []string{"dark", "light", "system"}
	themeNames := []string{T("Dark"), T("Light"), T("Same as Windows")}
	themeIdx := indexOf(themeKeys, strings.ToLower(c.Theme))
	if strings.ToLower(c.Theme) == "auto" {
		themeIdx = 2
	}
	langList := Languages()
	langNames := make([]string, len(langList))
	langIdx := 0
	for i, l := range langList {
		langNames[i] = l.Name
		if l.Code == CurrentLanguage() {
			langIdx = i
		}
	}
	res, err := Dialog{
		AssignTo:      &dlg,
		Title:         T("GoDM Options"),
		Icon:          appIcon(),
		DefaultButton: &okBtn,
		CancelButton:  &cancelBtn,
		MinSize:       Size{Width: 560, Height: 400},
		Layout:        VBox{},
		Children: []Widget{
			TabWidget{Pages: []TabPage{
				{Title: T("Connection"), Layout: Grid{Columns: 2}, Children: []Widget{
					Label{Text: T("Connections per download (more = faster):")},
					NumberEdit{AssignTo: &conns, MinValue: 1, MaxValue: 32, Value: float64(c.Connections), SpinButtonsVisible: true},
					Label{Text: T("Downloads at the same time:")},
					NumberEdit{AssignTo: &maxc, MinValue: 1, MaxValue: 32, Value: float64(c.MaxConcurrent), SpinButtonsVisible: true},
					Label{Text: T("Speed limit, KB/sec (0 = unlimited):")},
					NumberEdit{AssignTo: &limit, MinValue: 0, MaxValue: 10000000, Value: float64(c.SpeedLimit / 1024), SpinButtonsVisible: true},
					CheckBox{AssignTo: &retry, ColumnSpan: 2, Text: T("Retry failed downloads automatically (every 30 seconds)"), Checked: c.AutoRetry},
					VSpacer{ColumnSpan: 2},
				}},
				{Title: T("Save To"), Layout: VBox{}, Children: []Widget{
					Label{Text: T("Download folder:")},
					Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
						LineEdit{AssignTo: &dir, Text: c.DownloadDir},
						PushButton{Text: T("Browse..."), OnClicked: func() {
							fd := walk.FileDialog{Title: T("Choose the download folder"), FilePath: dir.Text()}
							if ok, _ := fd.ShowBrowseFolder(dlg); ok {
								dir.SetText(fd.FilePath)
							}
						}},
					}},
					CheckBox{AssignTo: &categorize, Text: T("Sort files into folders by type (Compressed, Documents, Music, Programs, Video)"), Checked: c.Categorize},
					VSpacer{},
				}},
				{Title: T("General"), Layout: VBox{}, Children: []Widget{
					CheckBox{AssignTo: &clip, Text: T("Catch download links copied to the clipboard"), Checked: c.MonitorClipboard},
					CheckBox{AssignTo: &complete, Text: T("Show the \"Download complete\" window"), Checked: c.ShowCompleteDialog},
					CheckBox{AssignTo: &ask, Text: T("Ask where to save downloads coming from the browser"), Checked: c.ShowBrowserDialog},
					CheckBox{AssignTo: &tray, Text: T("Close button keeps GoDM running in the tray"), Checked: c.CloseToTray},
					CheckBox{AssignTo: &run, Text: T("Start GoDM when Windows starts"), Checked: autostartEnabled()},
					VSpacer{},
				}},
				{Title: T("Appearance"), Layout: Grid{Columns: 2}, Children: []Widget{
					Label{Text: T("Theme:")},
					ComboBox{AssignTo: &themeCB, Model: themeNames, CurrentIndex: themeIdx},
					Label{Text: T("Language:")},
					ComboBox{AssignTo: &langCB, Model: langNames, CurrentIndex: langIdx},
					Label{ColumnSpan: 2, Text: T("Need another language? Add a .json file to the lang folder.")},
					PushButton{ColumnSpan: 2, Text: T("Open language folder"), OnClicked: a.openLanguageFolder},
					VSpacer{ColumnSpan: 2},
				}},
				{Title: T("Browser"), Layout: VBox{}, Children: []Widget{
					Label{Text: T("GoDM can take over downloads from Chrome, Edge, Brave and Firefox.")},
					Label{Text: T("1. Click \"Set up browser integration\" below.")},
					Label{Text: T("2. In the browser open the extensions page, turn on Developer mode,")},
					Label{Text: T("    click \"Load unpacked\" and choose the GoDM extension folder.")},
					Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
						PushButton{Text: T("Set up browser integration"), OnClicked: func() { a.installBrowser(dlg) }},
						PushButton{Text: T("Open extension folder"), OnClicked: func() { shellOpen(extensionDir()) }},
						HSpacer{},
					}},
					VSpacer{},
				}},
			}},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				HSpacer{},
				PushButton{AssignTo: &okBtn, Text: T("OK"), OnClicked: func() {
					cn, mc, lim := int(conns.Value()), int(maxc.Value()), int64(limit.Value())*1024
					folder := strings.TrimSpace(dir.Text())
					ar, cz := retry.Checked(), categorize.Checked()
					if err := a.m.UpdateSettings(manager.Settings{
						DownloadDir: &folder, Connections: &cn, MaxConcurrent: &mc, SpeedLimit: &lim, AutoRetry: &ar, Categorize: &cz,
					}); err != nil {
						walk.MsgBox(dlg, "Options", err.Error(), walk.MsgBoxIconWarning)
						return
					}
					mon, cmp, ak, tr := clip.Checked(), complete.Checked(), ask.Checked(), tray.Checked()
					newTheme, newLang := themeKeys[max(themeCB.CurrentIndex(), 0)], langList[max(langCB.CurrentIndex(), 0)].Code
					a.cfg.Update(func(c *config.Config) {
						c.MonitorClipboard, c.ShowCompleteDialog, c.ShowBrowserDialog, c.CloseToTray = mon, cmp, ak, tr
						c.Theme, c.Language = newTheme, newLang
					})
					if err := setAutostart(run.Checked()); err != nil {
						walk.MsgBox(dlg, "Options", "Could not change \"Start with Windows\": "+err.Error(), walk.MsgBoxIconWarning)
					}
					dlg.Accept()
				}},
				PushButton{AssignTo: &cancelBtn, Text: T("Cancel"), OnClicked: func() { dlg.Cancel() }},
			}},
		},
	}.Run(a.mw)
	if err == nil && res == walk.DlgCmdOK {
		a.applyTheme()
		a.applyLanguage()
	}
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(AppName)
	return err == nil
}

func setAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(AppName); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	return k.SetStringValue(AppName, `"`+exePath()+`" -tray`)
}

func extensionDir() string { return filepath.Join(filepath.Dir(exePath()), "extension") }

// installBrowser registers the console helper (godm.exe) for the extension.
func (a *App) installBrowser(owner walk.Form) {
	host := filepath.Join(filepath.Dir(exePath()), "godm-cli.exe")
	if _, err := os.Stat(host); err != nil {
		walk.MsgBox(owner, "Browser integration", "The helper program godm-cli.exe was not found next to GoDM.exe. Please reinstall GoDM.", walk.MsgBoxIconError)
		return
	}
	if _, err := nativehost.Install(host, []string{nativehost.ChromeExtensionID}, true); err != nil {
		walk.MsgBox(owner, "Browser integration", "Setup failed: "+err.Error(), walk.MsgBoxIconError)
		return
	}
	walk.MsgBox(owner, "Browser integration",
		"Done. Now add the GoDM extension to your browser:\n\n"+
			"Chrome / Edge / Brave:\n  1. Open chrome://extensions (Edge: edge://extensions)\n  2. Turn on \"Developer mode\"\n  3. Click \"Load unpacked\" and choose:\n     "+extensionDir()+
			"\n\nThen restart the browser. New downloads will open in GoDM.", walk.MsgBoxIconInformation)
	shellOpen(extensionDir())
}

func (a *App) browserHelpDialog() {
	if walk.MsgBox(a.mw, T("Browser integration"),
		T("GoDM catches downloads from Chrome, Edge, Brave and Firefox using a small extension.\n\nSet it up now?"),
		walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) == walk.DlgCmdYes {
		a.installBrowser(a.mw)
	}
}

// ---------------------------------------------------------------- Scheduler

func (a *App) schedulerDialog() {
	a.modal++
	defer func() { a.modal-- }()
	sc := a.cfg.Schedule
	if sc.Start == "" {
		sc.Start = "02:00"
	}
	if sc.Stop == "" {
		sc.Stop = "08:00"
	}
	doneOpts := []string{T("Do nothing"), T("Exit GoDM"), T("Shut down the computer")}
	doneVals := []string{"", "exit", "shutdown"}
	var (
		dlg              *walk.Dialog
		startOn, stopOn  *walk.CheckBox
		daily            *walk.CheckBox
		start, stop      *walk.LineEdit
		done             *walk.ComboBox
		okBtn, cancelBtn *walk.PushButton
	)
	res, err := Dialog{
		AssignTo:      &dlg,
		Title:         T("Scheduler"),
		Icon:          appIcon(),
		DefaultButton: &okBtn,
		CancelButton:  &cancelBtn,
		MinSize:       Size{Width: 440},
		Layout:        Grid{Columns: 2},
		Children: []Widget{
			CheckBox{AssignTo: &startOn, Text: T("Start download queue at (HH:MM)"), Checked: sc.StartEnabled},
			LineEdit{AssignTo: &start, Text: sc.Start, MaxLength: 5},
			CheckBox{AssignTo: &stopOn, Text: T("Stop download queue at (HH:MM)"), Checked: sc.StopEnabled},
			LineEdit{AssignTo: &stop, Text: sc.Stop, MaxLength: 5},
			CheckBox{AssignTo: &daily, ColumnSpan: 2, Text: T("Every day"), Checked: sc.Daily},
			Label{Text: T("When the queue is finished:")},
			ComboBox{AssignTo: &done, Model: doneOpts, CurrentIndex: indexOf(doneVals, sc.WhenDone)},
			Composite{ColumnSpan: 2, Layout: HBox{MarginsZero: true}, Children: []Widget{
				PushButton{Text: T("Start queue now"), OnClicked: func() { a.m.ResumeAll(); a.sched.running, a.sched.startedAt = true, time.Now() }},
				HSpacer{},
				PushButton{AssignTo: &okBtn, Text: T("Apply"), OnClicked: func() {
					ns := config.Schedule{
						StartEnabled: startOn.Checked(), Start: strings.TrimSpace(start.Text()),
						StopEnabled: stopOn.Checked(), Stop: strings.TrimSpace(stop.Text()),
						Daily: daily.Checked(),
					}
					if i := done.CurrentIndex(); i >= 0 && i < len(doneVals) {
						ns.WhenDone = doneVals[i]
					}
					for _, t := range []struct {
						on bool
						v  string
					}{{ns.StartEnabled, ns.Start}, {ns.StopEnabled, ns.Stop}} {
						if _, _, err := ParseClock(t.v); t.on && err != nil {
							walk.MsgBox(dlg, "Scheduler", fmt.Sprintf("%q: %v", t.v, err), walk.MsgBoxIconWarning)
							return
						}
					}
					a.cfg.Update(func(c *config.Config) { c.Schedule = ns })
					running := a.sched.running
					a.sched.set(ns, time.Now())
					a.sched.running = running
					dlg.Accept()
				}},
				PushButton{AssignTo: &cancelBtn, Text: T("Cancel"), OnClicked: func() { dlg.Cancel() }},
			}},
		},
	}.Run(a.mw)
	if err == nil && res == walk.DlgCmdOK {
		a.refresh()
	}
}

// schedActions for the App.
func (a *App) startQueue() { a.m.ResumeAll() }
func (a *App) stopQueue()  { a.m.PauseAll() }
func (a *App) exitApp() {
	a.quitting = true
	a.mw.Close()
}
func (a *App) shutdownPC() {
	cmd := exec.Command("shutdown.exe", "/s", "/t", "60", "/c", "GoDM finished all downloads. Windows will shut down in 1 minute (run \"shutdown /a\" to cancel).")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Start()
	a.exitApp()
}

// ---------------------------------------------------------------- About

func (a *App) aboutDialog() {
	walk.MsgBox(a.mw, "About GoDM",
		"GoDM "+Version+" — download manager written in Go\n\n"+
			"• Up to 32 connections per file with dynamic splitting\n"+
			"• Pause / resume, automatic retry, scheduler, speed limiter\n"+
			"• Catches links from the clipboard and the browser\n\n"+
			"Settings and log: "+a.home+"\n\nOpen source, MIT license.", walk.MsgBoxIconInformation)
}
