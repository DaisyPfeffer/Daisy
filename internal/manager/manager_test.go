package manager

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"godm/internal/config"
	"godm/internal/engine"
)

func newMgr(t *testing.T, home string, opts ...func(*config.Config)) (*Manager, *config.Config) {
	t.Helper()
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DownloadDir = filepath.Join(home, "dl")
	cfg.Categorize = false
	cfg.MaxConcurrent = 2
	for _, o := range opts {
		o(cfg)
	}
	m, err := New(cfg, home, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	m.Engine().RetryDelay = 10 * time.Millisecond
	return m, cfg
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func fileServer(data []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
}

func TestAddDownloadsAndCompletes(t *testing.T) {
	data := bytes.Repeat([]byte("goDM"), 100000)
	srv := fileServer(data)
	defer srv.Close()
	m, _ := newMgr(t, t.TempDir())
	defer m.Close()

	info, err := m.Add(engine.Request{URL: srv.URL + "/f.bin"}, AddOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		i, _ := m.Get(info.ID)
		return i.Status == engine.StatusCompleted
	})
	i, _ := m.Get(info.ID)
	got, _ := os.ReadFile(i.Path)
	if !bytes.Equal(got, data) {
		t.Fatal("content differs")
	}
}

func TestRejectsNonHTTPURLs(t *testing.T) {
	m, _ := newMgr(t, t.TempDir())
	defer m.Close()
	for _, u := range []string{"file:///etc/passwd", "ftp://x/y", "javascript:alert(1)", "notaurl", ""} {
		if _, err := m.Add(engine.Request{URL: u}, AddOptions{}); err == nil {
			t.Errorf("%q should be rejected", u)
		}
	}
}

func TestConcurrencyLimit(t *testing.T) {
	release := make(chan struct{})
	var active, peak int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-0" {
			http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader([]byte("x")))
			return
		}
		// probe request: hold it so the download stays "running"
		n := atomicAdd(&active, 1)
		if n > atomicLoad(&peak) {
			atomicStore(&peak, n)
		}
		<-release
		atomicAdd(&active, -1)
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader([]byte("x")))
	}))
	defer srv.Close()
	m, _ := newMgr(t, t.TempDir()) // MaxConcurrent = 2
	defer m.Close()
	for i := 0; i < 5; i++ {
		if _, err := m.Add(engine.Request{URL: srv.URL + "/f" + string(rune('a'+i))}, AddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(1200 * time.Millisecond)
	if p := atomicLoad(&peak); p != 2 {
		t.Fatalf("peak concurrent downloads = %d, want 2", p)
	}
	close(release)
}

func TestScheduledStartWaits(t *testing.T) {
	srv := fileServer([]byte("hello"))
	defer srv.Close()
	m, _ := newMgr(t, t.TempDir())
	defer m.Close()
	info, _ := m.Add(engine.Request{URL: srv.URL + "/f.bin"}, AddOptions{StartAt: time.Now().Add(1500 * time.Millisecond)})
	time.Sleep(700 * time.Millisecond)
	if i, _ := m.Get(info.ID); i.Status != engine.StatusQueued {
		t.Fatalf("status = %s before start time, want queued", i.Status)
	}
	waitFor(t, 8*time.Second, func() bool {
		i, _ := m.Get(info.ID)
		return i.Status == engine.StatusCompleted
	})
}

func TestFailedDownloadRetriesAutomatically(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader([]byte("ok")))
	}))
	defer srv.Close()
	m, _ := newMgr(t, t.TempDir(), func(c *config.Config) { c.RetryInterval = 1 })
	defer m.Close()
	info, _ := m.Add(engine.Request{URL: srv.URL + "/f.bin"}, AddOptions{})
	waitFor(t, 5*time.Second, func() bool {
		i, _ := m.Get(info.ID)
		return i.Status == engine.StatusError && !i.RetryAt.IsZero()
	})
	fail.Store(false)
	waitFor(t, 8*time.Second, func() bool {
		i, _ := m.Get(info.ID)
		return i.Status == engine.StatusCompleted
	})
}

func TestPermanentFailureIsNotRetried(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	m, _ := newMgr(t, t.TempDir(), func(c *config.Config) { c.RetryInterval = 1 })
	defer m.Close()
	info, _ := m.Add(engine.Request{URL: srv.URL + "/gone"}, AddOptions{})
	waitFor(t, 5*time.Second, func() bool {
		i, _ := m.Get(info.ID)
		return i.Status == engine.StatusError
	})
	if i, _ := m.Get(info.ID); !i.RetryAt.IsZero() {
		t.Fatal("a 404 must not be scheduled for retry")
	}
}

func TestStateSurvivesRestartAndPausedStaysPaused(t *testing.T) {
	srv := fileServer([]byte("data"))
	defer srv.Close()
	home := t.TempDir()
	m, _ := newMgr(t, home)
	info, _ := m.Add(engine.Request{URL: srv.URL + "/f.bin"}, AddOptions{Paused: true})
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m2, _ := newMgr(t, home)
	defer m2.Close()
	i, err := m2.Get(info.ID)
	if err != nil {
		t.Fatal("download lost after restart")
	}
	time.Sleep(800 * time.Millisecond)
	if i, _ = m2.Get(info.ID); i.Status != engine.StatusPaused {
		t.Fatalf("status = %s, want paused", i.Status)
	}
	m2.Resume(info.ID)
	waitFor(t, 8*time.Second, func() bool {
		i, _ := m2.Get(info.ID)
		return i.Status == engine.StatusCompleted
	})
}

func TestRemoveDeletesPartialAndOptionallyFile(t *testing.T) {
	srv := fileServer([]byte("hello world"))
	defer srv.Close()
	m, _ := newMgr(t, t.TempDir())
	defer m.Close()
	info, _ := m.Add(engine.Request{URL: srv.URL + "/f.bin"}, AddOptions{})
	waitFor(t, 8*time.Second, func() bool {
		i, _ := m.Get(info.ID)
		return i.Status == engine.StatusCompleted
	})
	i, _ := m.Get(info.ID)
	if err := m.Remove(info.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(i.Path); err != nil {
		t.Fatal("file must be kept when deleteFile=false")
	}
	info2, _ := m.Add(engine.Request{URL: srv.URL + "/f.bin"}, AddOptions{})
	waitFor(t, 8*time.Second, func() bool {
		x, _ := m.Get(info2.ID)
		return x.Status == engine.StatusCompleted
	})
	i2, _ := m.Get(info2.ID)
	m.Remove(info2.ID, true)
	if _, err := os.Stat(i2.Path); !os.IsNotExist(err) {
		t.Fatal("file must be deleted when deleteFile=true")
	}
	if err := m.Remove("nope", false); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSavedLoginIsApplied(t *testing.T) {
	var user, pass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ = r.BasicAuth()
		http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader([]byte("x")))
	}))
	defer srv.Close()
	m, _ := newMgr(t, t.TempDir(), func(c *config.Config) {
		c.Logins = map[string]config.Login{"127.0.0.1": {Username: "alice", Password: "s3cret"}}
	})
	defer m.Close()
	info, _ := m.Add(engine.Request{URL: srv.URL + "/f.bin"}, AddOptions{})
	waitFor(t, 8*time.Second, func() bool {
		i, _ := m.Get(info.ID)
		return i.Status == engine.StatusCompleted
	})
	if user != "alice" || pass != "s3cret" {
		t.Fatalf("server saw %q/%q", user, pass)
	}
}

func TestEditBeforeStartChangesSaveLocation(t *testing.T) {
	srv := fileServer([]byte("abc"))
	defer srv.Close()
	home := t.TempDir()
	m, _ := newMgr(t, home)
	defer m.Close()
	info, _ := m.Add(engine.Request{URL: srv.URL + "/f.bin"}, AddOptions{Paused: true})
	dir := filepath.Join(home, "chosen")
	if err := m.Edit(info.ID, func(r *engine.Request) { r.Dir = dir; r.FileName = "my:name.bin"; r.Description = "note" }); err != nil {
		t.Fatal(err)
	}
	m.Resume(info.ID)
	waitFor(t, 8*time.Second, func() bool {
		i, _ := m.Get(info.ID)
		return i.Status == engine.StatusCompleted
	})
	i, _ := m.Get(info.ID)
	if i.Path != filepath.Join(dir, "my_name.bin") || i.Description != "note" {
		t.Fatalf("path = %q desc = %q", i.Path, i.Description)
	}
	if err := m.Edit(info.ID, func(r *engine.Request) { r.FileName = "other.bin" }); err != ErrStarted {
		t.Fatalf("err = %v, want ErrStarted", err)
	}
	if err := m.Edit(info.ID, func(r *engine.Request) { r.Description = "still editable" }); err != nil {
		t.Fatalf("description edit after finish: %v", err)
	}
}

func TestRemoveCompletedKeepsUnfinished(t *testing.T) {
	srv := fileServer([]byte("abc"))
	defer srv.Close()
	m, _ := newMgr(t, t.TempDir())
	defer m.Close()
	done, _ := m.Add(engine.Request{URL: srv.URL + "/a.bin"}, AddOptions{})
	waiting, _ := m.Add(engine.Request{URL: srv.URL + "/b.bin"}, AddOptions{Paused: true})
	waitFor(t, 8*time.Second, func() bool {
		i, _ := m.Get(done.ID)
		return i.Status == engine.StatusCompleted
	})
	i, _ := m.Get(done.ID)
	if n := m.RemoveCompleted(); n != 1 {
		t.Fatalf("removed %d, want 1", n)
	}
	if _, err := m.Get(waiting.ID); err != nil {
		t.Fatal("unfinished download was removed")
	}
	if _, err := os.Stat(i.Path); err != nil {
		t.Fatal("finished file must stay on disk")
	}
}
