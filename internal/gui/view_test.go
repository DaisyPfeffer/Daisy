package gui

import (
	"testing"
	"time"

	"godm/internal/engine"
)

func TestCategoriesFilterDownloads(t *testing.T) {
	list := []engine.Info{
		{ID: "a", Request: engine.Request{URL: "http://x/movie.mkv"}, Status: engine.StatusActive, Size: 100},
		{ID: "b", Request: engine.Request{URL: "http://x/song.mp3", FileName: "song.mp3"}, Status: engine.StatusCompleted, Size: 5},
		{ID: "c", Request: engine.Request{URL: "http://x/pack.zip"}, Status: engine.StatusQueued, Size: -1},
		{ID: "d", Request: engine.Request{URL: "http://x/setup.exe"}, Status: engine.StatusPaused, Size: 10},
	}
	ids := func(c Category) string {
		s := ""
		for _, r := range BuildRows(list, c, time.Now()) {
			s += r.ID
		}
		return s
	}
	cases := map[Category]string{
		CatAll: "abcd", CatVideo: "a", CatMusic: "b", CatCompressed: "c", CatPrograms: "d",
		CatDocuments: "", CatFinished: "b", CatUnfinished: "acd", CatQueue: "ac",
	}
	for c, want := range cases {
		if got := ids(c); got != want {
			t.Errorf("%s: got %q, want %q", c.Title(), got, want)
		}
	}
}

func TestRowFormatting(t *testing.T) {
	now := time.Date(2026, 11, 9, 23, 0, 0, 0, time.Local)
	i := engine.Info{
		ID: "a", Request: engine.Request{URL: "http://x/f.iso"}, Status: engine.StatusActive,
		Size: 100 * 1024 * 1024, Downloaded: 25 * 1024 * 1024, Speed: 1024 * 1024, LastTry: now,
	}
	r := BuildRows([]engine.Info{i}, CatAll, now)[0]
	if r.FileName != "f.iso" || r.Size != "100.00 MB" || r.Status != "25.00%" {
		t.Fatalf("row = %+v", r)
	}
	if r.TimeLeft != "1 min 15 sec" || r.Rate != "1.00 MB/sec" || r.LastTry != "Nov 09 23:00" {
		t.Fatalf("time/rate = %q %q %q", r.TimeLeft, r.Rate, r.LastTry)
	}
	if r.Description != "http://x/f.iso" || r.Icon != "cat_programs" {
		t.Fatalf("desc/icon = %q %q", r.Description, r.Icon)
	}
}

func TestStatusTexts(t *testing.T) {
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.Local)
	cases := []struct {
		i    engine.Info
		want string
	}{
		{engine.Info{Status: engine.StatusCompleted}, "Complete"},
		{engine.Info{Status: engine.StatusPaused, Size: 200, Downloaded: 50}, "25.00%"},
		{engine.Info{Status: engine.StatusPaused, Size: -1}, "Stopped"},
		{engine.Info{Status: engine.StatusQueued, StartAt: now.Add(time.Hour)}, "Scheduled 11:00"},
		{engine.Info{Status: engine.StatusQueued}, "Queued"},
		{engine.Info{Status: engine.StatusActive, Size: -1}, "Connecting..."},
		{engine.Info{Status: engine.StatusError}, "Error"},
	}
	for _, c := range cases {
		if got := StatusText(c.i, now); got != c.want {
			t.Errorf("StatusText(%+v) = %q, want %q", c.i.Status, got, c.want)
		}
	}
}

func TestQueueNumbers(t *testing.T) {
	list := []engine.Info{
		{ID: "a", Status: engine.StatusQueued}, {ID: "b", Status: engine.StatusCompleted}, {ID: "c", Status: engine.StatusQueued},
	}
	rows := BuildRows(list, CatAll, time.Now())
	if rows[0].Q != "1" || rows[1].Q != "" || rows[2].Q != "2" {
		t.Fatalf("Q = %q %q %q", rows[0].Q, rows[1].Q, rows[2].Q)
	}
}

func TestDurationAndSizes(t *testing.T) {
	if Duration(42) != "42 sec" || Duration(185) != "3 min 5 sec" || Duration(7300) != "2 hr 1 min" {
		t.Fatal("duration format wrong")
	}
	if HumanSize(512) != "512 Bytes" || HumanSize(1536) != "1.50 KB" || HumanSize(3*1024*1024*1024) != "3.00 GB" {
		t.Fatal("size format wrong")
	}
	if SpeedText(800*1024) != "800.00 KB/sec" || SpeedText(0) != "0 KB/sec" {
		t.Fatal("speed format wrong")
	}
}

func TestClipboardCatching(t *testing.T) {
	yes := []string{"https://example.com/file.zip", "  http://x.org/a/b/Setup.EXE?x=1  ", "https://cdn.x/v.mp4"}
	no := []string{"https://example.com/page.html", "https://example.com/", "hello world", "ftp://x/a.zip",
		"https://x/a.zip https://x/b.zip", "C:\\files\\a.zip", ""}
	for _, s := range yes {
		if _, ok := CatchableURL(s); !ok {
			t.Errorf("%q should be caught", s)
		}
	}
	for _, s := range no {
		if _, ok := CatchableURL(s); ok {
			t.Errorf("%q should not be caught", s)
		}
	}
	got := ExtractURLs("get https://a/1.zip and (https://a/2.zip), again https://a/1.zip ftp://n/o")
	if len(got) != 2 || got[0] != "https://a/1.zip" || got[1] != "https://a/2.zip" {
		t.Fatalf("ExtractURLs = %v", got)
	}
}

func TestConnRows(t *testing.T) {
	i := engine.Info{Status: engine.StatusActive, Segments: []engine.Segment{
		{Start: 0, End: 1023, Pos: 1024}, {Start: 1024, End: 4095, Pos: 2048},
	}}
	rows := ConnRows(i)
	if len(rows) != 2 || rows[0].Info != "Finished" || rows[1].Info != "Receiving data..." || rows[1].Downloaded != "1.00 KB" {
		t.Fatalf("rows = %+v", rows)
	}
	i.Status = engine.StatusPaused
	if ConnRows(i)[1].Info != "Stopped" {
		t.Fatal("paused connection should read Stopped")
	}
}
