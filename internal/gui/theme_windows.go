//go:build windows

package gui

import (
	"bytes"
	"embed"
	"image"
	"image/png"
	"strings"
	"syscall"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Colors. They are changed by setPalette when the theme changes.
var (
	colBg      walk.Color
	colPanel   walk.Color
	colRowAlt  walk.Color
	colText    walk.Color
	colDim     walk.Color
	colPressed walk.Color
	colSel     walk.Color
	colAccent  walk.Color
	colDone    walk.Color
	colErr     walk.Color

	brushBg      *walk.SolidColorBrush
	brushPressed *walk.SolidColorBrush
	brushPanel   *walk.SolidColorBrush
	brushTrack   *walk.SolidColorBrush
	brushSeg     *walk.SolidColorBrush
	brushSegDone *walk.SolidColorBrush
	tbFont       *walk.Font
	boldFont     *walk.Font
)

// themeDark reports whether a theme name ("dark", "light" or "system") means dark.
func themeDark(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "light":
		return false
	case "system", "auto":
		return systemDark()
	}
	return true
}

// systemDark reads the Windows "app mode" setting (dark unless apps use the light theme).
func systemDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return true
	}
	return v == 0
}

// setPalette switches every color and brush to the dark or the light set.
func setPalette(dark bool) {
	if dark {
		colBg, colPanel, colRowAlt = walk.RGB(32, 32, 32), walk.RGB(45, 45, 45), walk.RGB(50, 50, 50)
		colText, colDim, colPressed = walk.RGB(232, 232, 232), walk.RGB(112, 112, 112), walk.RGB(62, 62, 62)
		colSel, colAccent = walk.RGB(0, 92, 168), walk.RGB(61, 160, 255)
		colDone, colErr = walk.RGB(110, 200, 120), walk.RGB(240, 110, 110)
	} else {
		colBg, colPanel, colRowAlt = walk.RGB(240, 240, 240), walk.RGB(255, 255, 255), walk.RGB(246, 247, 249)
		colText, colDim, colPressed = walk.RGB(28, 28, 28), walk.RGB(150, 150, 150), walk.RGB(214, 214, 214)
		colSel, colAccent = walk.RGB(0, 120, 215), walk.RGB(0, 102, 204)
		colDone, colErr = walk.RGB(22, 130, 45), walk.RGB(196, 43, 28)
	}
	brushBg, _ = walk.NewSolidColorBrush(colBg)
	brushPressed, _ = walk.NewSolidColorBrush(colPressed)
	brushPanel, _ = walk.NewSolidColorBrush(colPanel)
	brushTrack, _ = walk.NewSolidColorBrush(walk.RGB(225, 225, 225))
	brushSeg, _ = walk.NewSolidColorBrush(walk.RGB(30, 120, 220))
	brushSegDone, _ = walk.NewSolidColorBrush(walk.RGB(30, 120, 220))
}

func initTheme(name string) {
	setPalette(themeDark(name))
	tbFont, _ = walk.NewFont("Segoe UI", 10, 0)
	boldFont, _ = walk.NewFont("Segoe UI", 9, walk.FontBold)
}

//go:embed assets/*.png
var assets embed.FS

var bmpCache = map[string]*walk.Bitmap{}

func loadImage(name string) image.Image {
	b, err := assets.ReadFile("assets/" + name + ".png")
	if err != nil {
		return nil
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	return img
}

// bitmap returns an embedded icon, or nil when it is missing.
func bitmap(name string) *walk.Bitmap {
	if b, ok := bmpCache[name]; ok {
		return b
	}
	img := loadImage(name)
	if img == nil {
		return nil
	}
	b, err := walk.NewBitmapFromImageForDPI(img, 96)
	if err != nil {
		return nil
	}
	bmpCache[name] = b
	return b
}

func appIcon() *walk.Icon {
	img := loadImage("app")
	if img == nil {
		return nil
	}
	ic, err := walk.NewIconFromImageForDPI(img, 96)
	if err != nil {
		return nil
	}
	return ic
}

// ----- Windows 10/11 dark mode helpers (all best effort) -----

var (
	modUser32                 = syscall.NewLazyDLL("user32.dll")
	procFindWindowExW         = modUser32.NewProc("FindWindowExW")
	modDwm                    = syscall.NewLazyDLL("dwmapi.dll")
	procDwmSetWindowAttribute = modDwm.NewProc("DwmSetWindowAttribute")
	modKernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetProcAddress        = modKernel32.NewProc("GetProcAddress")
)

func windowsBuild() uint32 {
	v := windows.RtlGetVersion()
	if v == nil || v.MajorVersion < 10 {
		return 0
	}
	return v.BuildNumber
}

func uxthemeOrdinal(n uintptr) uintptr {
	h, err := syscall.LoadLibrary("uxtheme.dll")
	if err != nil {
		return 0
	}
	addr, _, _ := procGetProcAddress.Call(uintptr(h), n)
	return addr
}

// enableDarkApp makes menus and scroll bars follow the theme on Windows 10 1809 and later.
func enableDarkApp(dark bool) {
	defer func() { recover() }()
	if windowsBuild() < 17763 {
		return
	}
	mode := uintptr(0) // default (light)
	if dark {
		mode = 2 // ForceDark
	}
	if p := uxthemeOrdinal(135); p != 0 { // SetPreferredAppMode / AllowDarkModeForApp
		syscall.SyscallN(p, mode)
	}
	if p := uxthemeOrdinal(136); p != 0 { // FlushMenuThemes
		syscall.SyscallN(p)
	}
}

func allowDarkWindow(hwnd win.HWND, on bool) {
	defer func() { recover() }()
	if windowsBuild() < 17763 {
		return
	}
	v := uintptr(0)
	if on {
		v = 1
	}
	if p := uxthemeOrdinal(133); p != 0 { // AllowDarkModeForWindow
		syscall.SyscallN(p, uintptr(hwnd), v)
	}
}

// titleBar makes the window caption dark or light.
func titleBar(hwnd win.HWND, dark bool) {
	defer func() { recover() }()
	b := windowsBuild()
	if b < 17763 {
		return
	}
	attr := uintptr(20) // DWMWA_USE_IMMERSIVE_DARK_MODE
	if b < 18985 {
		attr = 19
	}
	v := int32(0)
	if dark {
		v = 1
	}
	procDwmSetWindowAttribute.Call(uintptr(hwnd), attr, uintptr(unsafe.Pointer(&v)), 4)
	// make Windows repaint the caption right away
	win.SetWindowPos(hwnd, 0, 0, 0, 0, 0, win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_FRAMECHANGED|win.SWP_NOACTIVATE)
}

func setTheme(hwnd win.HWND, name string) {
	if hwnd == 0 {
		return
	}
	win.SetWindowTheme(hwnd, syscall.StringToUTF16Ptr(name), nil)
}

func childWindows(parent win.HWND, class string) []win.HWND {
	cls := syscall.StringToUTF16Ptr(class)
	var out []win.HWND
	var after uintptr
	for i := 0; i < 8; i++ {
		h, _, _ := procFindWindowExW.Call(uintptr(parent), after, uintptr(unsafe.Pointer(cls)), 0)
		if h == 0 {
			break
		}
		out = append(out, win.HWND(h))
		after = h
	}
	return out
}

// styleTable paints the empty area, header and scroll bars of a TableView for the theme.
func styleTable(tv *walk.TableView, dark bool) {
	defer func() { recover() }()
	body, header := "Explorer", "ItemsView"
	if dark {
		body, header = "DarkMode_Explorer", "DarkMode_ItemsView"
	}
	for _, lv := range childWindows(tv.Handle(), "SysListView32") {
		win.SendMessage(lv, win.LVM_SETBKCOLOR, 0, uintptr(colPanel))
		win.SendMessage(lv, win.LVM_SETTEXTBKCOLOR, 0, uintptr(colPanel))
		win.SendMessage(lv, win.LVM_SETTEXTCOLOR, 0, uintptr(colText))
		allowDarkWindow(lv, dark)
		setTheme(lv, body)
		if hdr := win.HWND(win.SendMessage(lv, win.LVM_GETHEADER, 0, 0)); hdr != 0 {
			allowDarkWindow(hdr, dark)
			setTheme(hdr, header)
		}
		win.InvalidateRect(lv, nil, true)
	}
}

// keepTableStyled re-applies the colors (walk resets them on Windows theme changes).
func keepTableStyled(tv *walk.TableView, dark bool) {
	for _, lv := range childWindows(tv.Handle(), "SysListView32") {
		if win.SendMessage(lv, lvmGetBkColor, 0, 0) != uintptr(colPanel) {
			styleTable(tv, dark)
			return
		}
	}
}

const lvmGetBkColor = win.LVM_FIRST + 0 // LVM_GETBKCOLOR

func styleTree(tv *walk.TreeView, dark bool) {
	defer func() { recover() }()
	tv.SetBackground(brushPanel)
	tv.SendMessage(win.TVM_SETBKCOLOR, 0, uintptr(colPanel))
	tv.SendMessage(win.TVM_SETTEXTCOLOR, 0, uintptr(colText))
	allowDarkWindow(tv.Handle(), dark)
	if dark {
		setTheme(tv.Handle(), "DarkMode_Explorer")
	} else {
		setTheme(tv.Handle(), "Explorer")
	}
	win.InvalidateRect(tv.Handle(), nil, true)
}
