// Package gui is the native Windows desktop app. This file holds the
// platform-independent logic (rows, categories, text formatting) so it can
// be tested on any OS; the window code lives in *_windows.go files.
package gui

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"godm/internal/engine"
)

// FileTypes are the extensions offered in the "File type" list of the Download File Info window
// (any other extension can be typed in).
var FileTypes = []string{"", ".zip", ".rar", ".7z", ".gz", ".tar", ".exe", ".msi", ".iso", ".apk",
	".mp4", ".mkv", ".avi", ".mov", ".webm", ".mp3", ".flac", ".wav", ".m4a", ".ogg",
	".pdf", ".docx", ".xlsx", ".pptx", ".txt", ".jpg", ".png", ".gif", ".webp", ".bin"}

// Category is a node of the "Categories" tree.
type Category int

const (
	CatAll Category = iota
	CatCompressed
	CatDocuments
	CatMusic
	CatPrograms
	CatVideo
	CatUnfinished
	CatFinished
	CatQueue
)

// FileCategories are the folders downloads are sorted into.
var FileCategories = []string{"General", "Compressed", "Documents", "Music", "Programs", "Video"}

func (c Category) Title() string {
	return [...]string{"All Downloads", "Compressed", "Documents", "Music", "Programs", "Video",
		"Unfinished", "Finished", "Queues"}[c]
}

// IconName is the asset used for the category in the tree.
func (c Category) IconName() string {
	return [...]string{"cat_all", "cat_compressed", "cat_documents", "cat_music", "cat_programs", "cat_video",
		"cat_unfinished", "cat_finished", "cat_queue"}[c]
}

// Match reports whether a download belongs in the category.
func (c Category) Match(i engine.Info) bool {
	switch c {
	case CatAll:
		return true
	case CatUnfinished:
		return i.Status != engine.StatusCompleted
	case CatFinished:
		return i.Status == engine.StatusCompleted
	case CatQueue:
		return i.Status == engine.StatusQueued || i.Status == engine.StatusActive
	default:
		return engine.Category(DisplayName(i)) == c.Title()
	}
}

// FileCategoryIcon returns the icon asset for a file name.
func FileCategoryIcon(name string) string {
	return "cat_" + strings.ToLower(engine.Category(name))
}

// DisplayName is the file name shown in the list.
func DisplayName(i engine.Info) string {
	if i.FileName != "" {
		return i.FileName
	}
	return engine.FileNameFrom("", i.URL, "")
}

// Row is one line of the download list, already formatted for display.
type Row struct {
	ID          string
	FileName    string
	Q           string
	Size        string
	Status      string
	TimeLeft    string
	Rate        string
	LastTry     string
	Description string
	Icon        string
	Info        engine.Info
}

// Column titles, in display order.
var Columns = []string{"File Name", "Q", "Size", "Status", "Time left", "Transfer rate", "Last Try Date", "Description"}

func (r Row) Cell(col int) string {
	switch col {
	case 0:
		return r.FileName
	case 1:
		return r.Q
	case 2:
		return r.Size
	case 3:
		return r.Status
	case 4:
		return r.TimeLeft
	case 5:
		return r.Rate
	case 6:
		return r.LastTry
	case 7:
		return r.Description
	}
	return ""
}

// BuildRows filters and formats downloads for the list. list must be oldest first.
func BuildRows(list []engine.Info, cat Category, now time.Time) []Row {
	q := 0
	queuePos := map[string]int{}
	for _, i := range list {
		if i.Status == engine.StatusQueued {
			q++
			queuePos[i.ID] = q
		}
	}
	rows := make([]Row, 0, len(list))
	for _, i := range list {
		if !cat.Match(i) {
			continue
		}
		name := DisplayName(i)
		r := Row{
			ID:          i.ID,
			FileName:    name,
			Size:        SizeText(i),
			Status:      StatusText(i, now),
			TimeLeft:    TimeLeftText(i),
			Rate:        RateText(i),
			LastTry:     LastTryText(i),
			Description: i.Description,
			Icon:        FileCategoryIcon(name),
			Info:        i,
		}
		if r.Description == "" {
			r.Description = i.URL
		}
		if p, ok := queuePos[i.ID]; ok {
			r.Q = fmt.Sprint(p)
		}
		rows = append(rows, r)
	}
	return rows
}

// HumanSize formats bytes like "1.25 MB".
func HumanSize(n int64) string {
	if n < 0 {
		return ""
	}
	if n < 1024 {
		return fmt.Sprintf("%d Bytes", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	f := float64(n) / 1024
	u := 0
	for f >= 1024 && u < len(units)-1 {
		f /= 1024
		u++
	}
	return fmt.Sprintf("%.2f %s", f, units[u])
}

func SizeText(i engine.Info) string {
	if i.Size >= 0 {
		return HumanSize(i.Size)
	}
	return ""
}

func Percent(i engine.Info) float64 {
	if i.Status == engine.StatusCompleted {
		return 100
	}
	if i.Size <= 0 {
		return 0
	}
	p := float64(i.Downloaded) / float64(i.Size) * 100
	if p > 100 {
		p = 100
	}
	return p
}

func StatusText(i engine.Info, now time.Time) string {
	switch i.Status {
	case engine.StatusCompleted:
		return "Complete"
	case engine.StatusActive:
		if i.Size > 0 {
			return fmt.Sprintf("%.2f%%", Percent(i))
		}
		if i.Downloaded > 0 {
			return HumanSize(i.Downloaded)
		}
		return "Connecting..."
	case engine.StatusQueued:
		if !i.StartAt.IsZero() && now.Before(i.StartAt) {
			return "Scheduled " + i.StartAt.Format("15:04")
		}
		return "Queued"
	case engine.StatusError:
		if !i.RetryAt.IsZero() {
			return "Retry " + i.RetryAt.Format("15:04:05")
		}
		return "Error"
	case engine.StatusPaused:
		if i.Size > 0 && i.Downloaded > 0 {
			return fmt.Sprintf("%.2f%%", Percent(i))
		}
		return "Stopped"
	}
	return string(i.Status)
}

// Duration formats seconds the way download managers do: "3 min 12 sec".
func Duration(sec float64) string {
	if sec < 0 || sec != sec || sec > 1e9 {
		return ""
	}
	s := int64(sec + 0.5)
	h, m := s/3600, s%3600/60
	s %= 60
	switch {
	case h > 0:
		return fmt.Sprintf("%d hr %d min", h, m)
	case m > 0:
		return fmt.Sprintf("%d min %d sec", m, s)
	default:
		return fmt.Sprintf("%d sec", s)
	}
}

func TimeLeftText(i engine.Info) string {
	if i.Status != engine.StatusActive || i.Speed <= 0 || i.Size <= 0 {
		return ""
	}
	return Duration(float64(i.Size-i.Downloaded) / float64(i.Speed))
}

func RateText(i engine.Info) string {
	if i.Status != engine.StatusActive {
		return ""
	}
	return SpeedText(i.Speed)
}

func SpeedText(bps int64) string {
	if bps <= 0 {
		return "0 KB/sec"
	}
	if bps >= 1024*1024 {
		return fmt.Sprintf("%.2f MB/sec", float64(bps)/1024/1024)
	}
	return fmt.Sprintf("%.2f KB/sec", float64(bps)/1024)
}

func LastTryText(i engine.Info) string {
	t := i.LastTry
	if t.IsZero() {
		t = i.CreatedAt
	}
	if t.IsZero() {
		return ""
	}
	return t.Format("Jan 02 15:04")
}

// ConnRow is one line of the "connections" table in the progress window.
type ConnRow struct {
	N          string
	Downloaded string
	Info       string
}

func ConnRows(i engine.Info) []ConnRow {
	var out []ConnRow
	for n, s := range i.Segments {
		if s.End-s.Start > 1<<50 { // unknown size (single stream)
			s.End = s.Pos
		}
		done := s.Pos - s.Start
		if done < 0 {
			done = 0
		}
		if max := s.End - s.Start + 1; done > max {
			done = max
		}
		info := "Receiving data..."
		switch {
		case s.Pos > s.End:
			info = "Finished"
		case i.Status != engine.StatusActive:
			info = "Stopped"
		}
		out = append(out, ConnRow{N: fmt.Sprint(n + 1), Downloaded: HumanSize(done), Info: info})
	}
	return out
}

// CatchExtensions are file types picked up from the clipboard automatically.
var CatchExtensions = map[string]bool{
	".zip": true, ".rar": true, ".7z": true, ".gz": true, ".tar": true, ".tgz": true, ".bz2": true, ".xz": true,
	".exe": true, ".msi": true, ".iso": true, ".img": true, ".dmg": true, ".apk": true, ".deb": true, ".rpm": true,
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".webm": true, ".flv": true, ".wmv": true, ".m4v": true,
	".mp3": true, ".flac": true, ".wav": true, ".aac": true, ".ogg": true, ".m4a": true,
	".pdf": true, ".epub": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
	".bin": true, ".dat": true, ".torrent": true,
}

// CatchableURL reports whether clipboard text is a single download link.
func CatchableURL(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 4096 || strings.ContainsAny(text, " \t\r\n") {
		return "", false
	}
	u, err := url.Parse(text)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	if !CatchExtensions[strings.ToLower(path.Ext(u.Path))] {
		return "", false
	}
	return u.String(), true
}

// ExtractURLs returns every http(s) link in a block of text (batch add).
func ExtractURLs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.Fields(text) {
		f = strings.Trim(f, `"'<>()[],;`)
		u, err := url.Parse(f)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// SameIDs reports whether two row lists contain the same downloads in order.
func SameIDs(a, b []Row) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

// Summary is the text for the status bar.
func Summary(list []engine.Info) (active int, speed int64, queued int) {
	for _, i := range list {
		switch i.Status {
		case engine.StatusActive:
			active++
			speed += i.Speed
		case engine.StatusQueued:
			queued++
		}
	}
	return
}
