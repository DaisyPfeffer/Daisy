package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func testData(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func rangeServer(data []byte, name string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		http.ServeContent(w, r, name, time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
}

func newEngine(dir string) *Engine {
	e := New()
	e.DefaultDir = dir
	e.RetryDelay = 10 * time.Millisecond
	return e
}

func TestMultiConnectionDownloadIsByteExact(t *testing.T) {
	data := testData(5*1024*1024 + 123)
	srv := rangeServer(data, "big.bin")
	defer srv.Close()
	dir := t.TempDir()
	e := newEngine(dir)
	task := NewTask("t", Request{URL: srv.URL + "/x", Connections: 8})
	if err := e.Run(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	i := task.Snapshot()
	got, err := os.ReadFile(i.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("downloaded bytes differ")
	}
	if filepath.Base(i.Path) != "big.bin" {
		t.Fatalf("name = %q, want big.bin", filepath.Base(i.Path))
	}
	if i.Downloaded != int64(len(data)) {
		t.Fatalf("Downloaded = %d, want %d", i.Downloaded, len(data))
	}
	if _, err := os.Stat(i.Path + PartSuffix); !os.IsNotExist(err) {
		t.Fatal("part file should be gone")
	}
}

func TestServerWithoutRangesFallsBackToStream(t *testing.T) {
	data := testData(300 * 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(data) // ignores Range entirely
	}))
	defer srv.Close()
	e := newEngine(t.TempDir())
	task := NewTask("t", Request{URL: srv.URL + "/plain.dat"})
	if err := e.Run(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	i := task.Snapshot()
	got, _ := os.ReadFile(i.Path)
	if !bytes.Equal(got, data) {
		t.Fatal("bytes differ")
	}
	if i.Resumable {
		t.Fatal("should not be resumable")
	}
}

func TestResumeContinuesWhereItStopped(t *testing.T) {
	data := testData(4 * 1024 * 1024)
	var requests atomic.Int64
	var bytesServed atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("ETag", `"v1"`)
		cw := &countingWriter{ResponseWriter: w, n: &bytesServed}
		http.ServeContent(cw, r, "r.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer srv.Close()

	dir := t.TempDir()
	e := newEngine(dir)
	e.Limiter = NewLimiter(2 * 1024 * 1024) // slow enough to interrupt
	task := NewTask("t", Request{URL: srv.URL + "/r.bin", Connections: 4})

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	err := e.Run(ctx, task)
	if err == nil {
		t.Fatal("expected interruption")
	}
	mid := task.Snapshot()
	if mid.Downloaded == 0 || mid.Downloaded >= int64(len(data)) {
		t.Fatalf("expected partial progress, got %d", mid.Downloaded)
	}

	// Persist and restore like the manager does after a restart.
	restored := TaskFromInfo(mid)
	bytesServed.Store(0)
	e.Limiter = nil
	if err := e.Run(context.Background(), restored); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(restored.Snapshot().Path)
	if !bytes.Equal(got, data) {
		t.Fatal("resumed file differs")
	}
	// Second phase must fetch (roughly) only the remainder, not everything again.
	if served := bytesServed.Load(); served > int64(len(data))-mid.Downloaded+512*1024 {
		t.Fatalf("resume re-downloaded too much: served %d, remaining was %d", served, int64(len(data))-mid.Downloaded)
	}
}

type countingWriter struct {
	http.ResponseWriter
	n *atomic.Int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n.Add(int64(n))
	return n, err
}

func TestRetriesTransientServerErrors(t *testing.T) {
	data := testData(1024 * 1024)
	var fails atomic.Int64
	fails.Store(3)
	inner := rangeServer(data, "flaky.bin")
	defer inner.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-0" && fails.Add(-1) >= 0 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	e := newEngine(t.TempDir())
	task := NewTask("t", Request{URL: srv.URL + "/f", Connections: 2})
	if err := e.Run(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(task.Snapshot().Path)
	if !bytes.Equal(got, data) {
		t.Fatal("bytes differ after retries")
	}
}

func TestPermanentErrorIsNotRetried(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	e := newEngine(t.TempDir())
	err := e.Run(context.Background(), NewTask("t", Request{URL: srv.URL + "/missing"}))
	var he HTTPError
	if !errors.As(err, &he) || he.Status != 404 {
		t.Fatalf("err = %v, want HTTP 404", err)
	}
	if !IsPermanent(err) {
		t.Fatal("404 must be permanent")
	}
	if IsPermanent(HTTPError{503}) || IsPermanent(HTTPError{429}) {
		t.Fatal("503 and 429 must be retryable")
	}
}

func TestChecksumVerification(t *testing.T) {
	data := testData(100 * 1024)
	srv := rangeServer(data, "c.bin")
	defer srv.Close()
	sum := sha256.Sum256(data)

	e := newEngine(t.TempDir())
	ok := NewTask("a", Request{URL: srv.URL + "/c", Checksum: "sha256:" + hex.EncodeToString(sum[:])})
	if err := e.Run(context.Background(), ok); err != nil {
		t.Fatalf("good checksum rejected: %v", err)
	}
	bad := NewTask("b", Request{URL: srv.URL + "/c", Checksum: "sha256:" + hex.EncodeToString(make([]byte, 32))})
	err := e.Run(context.Background(), bad)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	if _, statErr := os.Stat(bad.Snapshot().Path); statErr == nil {
		t.Fatal("corrupt file must not be moved to its final name")
	}
}

func TestSendsRefererCookieAndBasicAuth(t *testing.T) {
	var gotRef, gotCookie, gotUser, gotPass, gotCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRef, gotCookie = r.Header.Get("Referer"), r.Header.Get("Cookie")
		gotUser, gotPass, _ = r.BasicAuth()
		gotCustom = r.Header.Get("X-Token")
		http.ServeContent(w, r, "a.txt", time.Time{}, bytes.NewReader([]byte("hello")))
	}))
	defer srv.Close()
	e := newEngine(t.TempDir())
	task := NewTask("t", Request{URL: srv.URL + "/a.txt", Referer: "https://site.example/", Cookie: "sid=42",
		Username: "bob", Password: "pw", Headers: map[string]string{"X-Token": "abc"}})
	if err := e.Run(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if gotRef != "https://site.example/" || gotCookie != "sid=42" || gotUser != "bob" || gotPass != "pw" || gotCustom != "abc" {
		t.Fatalf("headers not forwarded: %q %q %q %q %q", gotRef, gotCookie, gotUser, gotPass, gotCustom)
	}
}

func TestSpeedLimitSlowsDownload(t *testing.T) {
	data := testData(512 * 1024)
	srv := rangeServer(data, "s.bin")
	defer srv.Close()
	e := newEngine(t.TempDir())
	task := NewTask("t", Request{URL: srv.URL + "/s", SpeedLimit: 256 * 1024}) // 256 KB/s -> ~2s
	start := time.Now()
	if err := e.Run(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 1500*time.Millisecond {
		t.Fatalf("finished in %v, limit not applied", d)
	}
}

func TestSegmentStealingSplitsLargestRemainder(t *testing.T) {
	task := NewTask("t", Request{})
	task.segs = []*Segment{{Start: 0, End: 999, Pos: 0}, {Start: 1000, End: 1099, Pos: 1100}}
	ns := task.steal(100)
	if ns == nil {
		t.Fatal("expected a stolen segment")
	}
	if ns.Start != 500 || ns.End != 999 || task.segs[0].End != 499 {
		t.Fatalf("bad split: new=%+v victim=%+v", *ns, *task.segs[0])
	}
	if task.steal(1000) != nil {
		t.Fatal("must not split below the minimum size")
	}
}

func TestFileNames(t *testing.T) {
	cases := []struct{ cd, url, ct, want string }{
		{`attachment; filename="report final.pdf"`, "http://x/y", "", "report final.pdf"},
		{`attachment; filename*=UTF-8''na%C3%AFve.txt`, "http://x/y", "", "naïve.txt"},
		{`attachment; filename="../../etc/passwd"`, "http://x/y", "", "passwd"},
		{"", "http://x/a/b/file%20name.zip?q=1", "", "file name.zip"},
		{"", "http://x/", "application/pdf", "download.pdf"},
		{`attachment; filename="a:b*c?.txt"`, "http://x/", "", "a_b_c_.txt"},
	}
	for _, c := range cases {
		if got := FileNameFrom(c.cd, c.url, c.ct); got != c.want {
			t.Errorf("FileNameFrom(%q,%q,%q) = %q, want %q", c.cd, c.url, c.ct, got, c.want)
		}
	}
	if SanitizeFileName("CON") != "_CON" {
		t.Error("reserved Windows name not escaped")
	}
	if Category("movie.MKV") != "Video" || Category("x.unknown") != "General" {
		t.Error("category mapping wrong")
	}
}

func TestDuplicateNamesGetNumbered(t *testing.T) {
	data := testData(1000)
	srv := rangeServer(data, "same.bin")
	defer srv.Close()
	dir := t.TempDir()
	e := newEngine(dir)
	var paths []string
	for i := 0; i < 3; i++ {
		task := NewTask("t", Request{URL: srv.URL + "/same.bin"})
		if err := e.Run(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filepath.Base(task.Snapshot().Path))
	}
	want := []string{"same.bin", "same (1).bin", "same (2).bin"}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
	}
}
