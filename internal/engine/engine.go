// Package engine implements a resumable, multi-connection HTTP(S) downloader
// with dynamic segment splitting, retries and bandwidth limiting.
package engine

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const PartSuffix = ".godmpart"

var (
	ErrNoRange         = errors.New("server stopped honouring byte ranges")
	ErrChanged         = errors.New("file changed on the server")
	ErrChecksum        = errors.New("checksum mismatch")
	ErrIncomplete      = errors.New("download incomplete")
	DefaultUserAgent   = "GoDM/1.0 (+https://github.com/godm)"
	unknownSizeEnd     = int64(math.MaxInt64 - 1)
	defaultBufferBytes = 64 * 1024
)

// HTTPError is returned for unexpected HTTP status codes.
type HTTPError struct{ Status int }

func (e HTTPError) Error() string {
	return fmt.Sprintf("server returned %d %s", e.Status, http.StatusText(e.Status))
}

// IsPermanent reports whether retrying the same request is pointless.
func IsPermanent(err error) bool {
	var he HTTPError
	if errors.As(err, &he) {
		return he.Status >= 400 && he.Status < 500 && he.Status != 408 && he.Status != 429
	}
	return errors.Is(err, ErrChecksum)
}

type Engine struct {
	Client      *http.Client
	Limiter     *Limiter // global limit shared by all downloads (may be nil)
	DefaultDir  string
	Categorize  bool // save into Compressed/, Video/, ... sub-folders
	Connections int  // default connections per download
	Retries     int  // retries per segment
	MinSplit    int64
	IdleTimeout time.Duration
	RetryDelay  time.Duration
}

func New() *Engine {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		// Byte ranges must refer to the raw bytes, never a gzip-decoded stream.
		DisableCompression: true,
		// HTTP/2 multiplexes everything over one TCP connection, which defeats
		// the point of parallel connections; stick to HTTP/1.1.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	return &Engine{
		Client:      &http.Client{Transport: tr},
		Connections: 8,
		Retries:     5,
		MinSplit:    256 * 1024,
		IdleTimeout: 30 * time.Second,
		RetryDelay:  time.Second,
	}
}

type probeResult struct {
	Size     int64
	Ranges   bool
	ETag     string
	LastMod  string
	FinalURL string
	FileName string
}

func (e *Engine) newRequest(ctx context.Context, info Info, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	ua := info.UserAgent
	if ua == "" {
		ua = DefaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept-Encoding", "identity")
	if info.Referer != "" {
		req.Header.Set("Referer", info.Referer)
	}
	if info.Cookie != "" {
		req.Header.Set("Cookie", info.Cookie)
	}
	for k, v := range info.Headers {
		req.Header.Set(k, v)
	}
	if info.Username != "" {
		req.SetBasicAuth(info.Username, info.Password)
	}
	return req, nil
}

// FileInfo is what the server tells us about a link before downloading.
type FileInfo struct {
	Size      int64 // -1 when unknown
	Resumable bool
	FileName  string
	FinalURL  string
}

// Probe asks the server for the size, name and resume support of a link.
func (e *Engine) Probe(ctx context.Context, req Request) (FileInfo, error) {
	pr, err := e.probe(ctx, Info{Request: req})
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Size: pr.Size, Resumable: pr.Ranges, FileName: pr.FileName, FinalURL: pr.FinalURL}, nil
}

// probe asks for the first byte to learn the size, range support and name.
func (e *Engine) probe(ctx context.Context, info Info) (*probeResult, error) {
	req, err := e.newRequest(ctx, info, info.URL)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	io.CopyN(io.Discard, resp.Body, 1024)

	pr := &probeResult{
		Size:     -1,
		ETag:     resp.Header.Get("ETag"),
		LastMod:  resp.Header.Get("Last-Modified"),
		FinalURL: resp.Request.URL.String(),
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
			pr.Size, pr.Ranges = total, true
		}
	case http.StatusOK:
		pr.Size = resp.ContentLength
	case http.StatusRequestedRangeNotSatisfiable:
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok && total == 0 {
			pr.Size = 0
		} else {
			return nil, HTTPError{resp.StatusCode}
		}
	default:
		return nil, HTTPError{resp.StatusCode}
	}
	pr.FileName = FileNameFrom(resp.Header.Get("Content-Disposition"), pr.FinalURL, resp.Header.Get("Content-Type"))
	return pr, nil
}

func parseContentRangeTotal(v string) (int64, bool) {
	i := strings.LastIndexByte(v, '/')
	if i < 0 || v[i+1:] == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v[i+1:]), 10, 64)
	return n, err == nil && n >= 0
}

func parseContentRangeStart(v string) (int64, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "bytes ")
	i := strings.IndexByte(v, '-')
	if i < 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(v[:i], 10, 64)
	return n, err == nil
}

func sameValidator(oldETag, newETag, oldLM, newLM string) bool {
	if oldETag != "" && newETag != "" {
		return oldETag == newETag
	}
	if oldLM != "" && newLM != "" {
		return oldLM == newLM
	}
	return true
}

// reservePath finds a free name in dir and creates its part file so two
// downloads can never pick the same name.
func reservePath(dir, name string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for n := 0; n < 10000; n++ {
		cand := name
		if n > 0 {
			cand = fmt.Sprintf("%s (%d)%s", base, n, ext)
		}
		p := filepath.Join(dir, cand)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		f, err := os.OpenFile(p+PartSuffix, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		f.Close()
		return p, nil
	}
	return "", errors.New("no free file name")
}

// Run downloads (or resumes) a task. It returns nil once the file is complete
// at Info.Path, or ctx's error if it was cancelled.
func (e *Engine) Run(ctx context.Context, t *Task) error {
	info := t.Snapshot()
	pr, err := e.probe(ctx, info)
	if err != nil {
		return err
	}

	t.mu.Lock()
	if len(t.segs) > 0 && (t.info.Size != pr.Size || !pr.Ranges ||
		!sameValidator(t.info.ETag, pr.ETag, t.info.LastModified, pr.LastMod)) {
		t.segs = nil // cannot safely resume: start over
	}
	t.info.Size, t.info.Resumable = pr.Size, pr.Ranges
	t.info.ETag, t.info.LastModified, t.info.FinalURL = pr.ETag, pr.LastMod, pr.FinalURL
	if t.info.FileName == "" {
		t.info.FileName = pr.FileName
	}
	fresh := len(t.segs) == 0
	info = t.info
	t.mu.Unlock()

	if info.Path == "" {
		dir := info.Dir
		if dir == "" {
			dir = e.DefaultDir
			if e.Categorize {
				dir = filepath.Join(dir, Category(info.FileName))
			}
		}
		p, err := reservePath(dir, SanitizeFileName(info.FileName))
		if err != nil {
			return err
		}
		t.Update(func(i *Info) { i.Path = p; i.FileName = filepath.Base(p) })
		info.Path = p
	}

	part := info.Path + PartSuffix
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			f.Sync()
			f.Close()
		}
	}()

	conns := info.Connections
	if conns <= 0 {
		conns = e.Connections
	}
	if conns <= 0 {
		conns = 1
	}
	taskLimit := NewLimiter(info.SpeedLimit)
	multi := pr.Ranges && pr.Size > 0

	if fresh {
		if err := f.Truncate(0); err != nil {
			return err
		}
		var segs []*Segment
		if multi {
			if err := f.Truncate(pr.Size); err != nil { // sparse pre-allocation
				return err
			}
			n := int(pr.Size / e.MinSplit)
			if n < 1 {
				n = 1
			}
			if n > conns {
				n = conns
			}
			chunk := pr.Size / int64(n)
			for i := 0; i < n; i++ {
				s := int64(i) * chunk
				end := s + chunk - 1
				if i == n-1 {
					end = pr.Size - 1
				}
				segs = append(segs, &Segment{Start: s, End: end, Pos: s})
			}
		} else if pr.Size != 0 {
			end := unknownSizeEnd
			if pr.Size > 0 {
				end = pr.Size - 1
			}
			segs = []*Segment{{Start: 0, End: end, Pos: 0}}
		}
		t.mu.Lock()
		t.segs = segs
		t.mu.Unlock()
	}

	if multi {
		err = e.runSegments(ctx, t, f, pr, conns, taskLimit)
	} else if pr.Size != 0 {
		err = e.stream(ctx, t, f, pr, taskLimit)
	}
	if err != nil {
		return err
	}
	if !t.complete() {
		return ErrIncomplete
	}

	if err := f.Sync(); err != nil {
		return err
	}
	closed = true
	if err := f.Close(); err != nil {
		return err
	}
	if info.Checksum != "" {
		if err := verifyChecksum(part, info.Checksum); err != nil {
			return err
		}
	}
	if err := os.Rename(part, info.Path); err != nil {
		return err
	}
	if lm, err := http.ParseTime(pr.LastMod); err == nil {
		os.Chtimes(info.Path, lm, lm)
	}
	return nil
}

func (e *Engine) runSegments(ctx context.Context, t *Task, f *os.File, pr *probeResult, conns int, tl *Limiter) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	pending := t.pending()
	work := make(chan *Segment, len(pending))
	for _, s := range pending {
		work <- s
	}
	close(work)

	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	// Run `conns` workers. Workers with no segment of their own split the
	// largest remaining one, so connections stay busy until the very end.
	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				var s *Segment
				select {
				case s = <-work:
				default:
				}
				if s == nil {
					s = t.steal(e.MinSplit)
				}
				if s == nil {
					return
				}
				if err := e.segmentWithRetry(ctx, t, f, pr, s, tl); err != nil {
					once.Do(func() { firstErr = err; cancel() })
					return
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func (e *Engine) segmentWithRetry(ctx context.Context, t *Task, f *os.File, pr *probeResult, s *Segment, tl *Limiter) error {
	attempt := 0
	for {
		t.mu.Lock()
		before := s.Pos
		t.mu.Unlock()

		err := e.fetchRange(ctx, t, f, pr, s, tl)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if IsPermanent(err) || errors.Is(err, ErrChanged) || errors.Is(err, ErrNoRange) {
			return err
		}
		t.mu.Lock()
		progressed := s.Pos > before
		t.mu.Unlock()
		if progressed {
			attempt = 0
		}
		attempt++
		if attempt > e.Retries {
			return err
		}
		delay := e.RetryDelay * time.Duration(1<<uint(attempt-1))
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (e *Engine) fetchRange(ctx context.Context, t *Task, f *os.File, pr *probeResult, s *Segment, tl *Limiter) error {
	t.mu.Lock()
	pos, end := s.Pos, s.End
	info := t.info
	t.mu.Unlock()
	if pos > end {
		return nil
	}

	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := time.AfterFunc(e.IdleTimeout, cancel)
	defer idle.Stop()

	req, err := e.newRequest(actx, info, pr.FinalURL)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", pos, end))
	if pr.ETag != "" && !strings.HasPrefix(pr.ETag, "W/") {
		req.Header.Set("If-Range", pr.ETag)
	} else if pr.LastMod != "" {
		req.Header.Set("If-Range", pr.LastMod)
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		if req.Header.Get("If-Range") != "" {
			return ErrChanged
		}
		return ErrNoRange
	default:
		return HTTPError{resp.StatusCode}
	}
	if start, ok := parseContentRangeStart(resp.Header.Get("Content-Range")); !ok || start != pos {
		return ErrNoRange
	}

	buf := make([]byte, defaultBufferBytes)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			idle.Reset(e.IdleTimeout)
			t.mu.Lock()
			end = s.End // may have shrunk because another connection stole the tail
			t.mu.Unlock()
			if pos > end {
				return nil
			}
			allowed := int64(n)
			if left := end - pos + 1; allowed > left {
				allowed = left
			}
			if err := e.Limiter.Wait(ctx, int(allowed)); err != nil {
				return err
			}
			if err := tl.Wait(ctx, int(allowed)); err != nil {
				return err
			}
			if _, err := f.WriteAt(buf[:allowed], pos); err != nil {
				return err
			}
			pos += allowed
			t.mu.Lock()
			s.Pos = pos
			end = s.End
			t.mu.Unlock()
			if pos > end {
				return nil
			}
		}
		if rerr == io.EOF {
			return io.ErrUnexpectedEOF
		}
		if rerr != nil {
			if actx.Err() != nil && ctx.Err() == nil {
				return fmt.Errorf("connection idle for %s", e.IdleTimeout)
			}
			return rerr
		}
	}
}

// stream handles servers without range support: one connection, from zero.
func (e *Engine) stream(ctx context.Context, t *Task, f *os.File, pr *probeResult, tl *Limiter) error {
	t.mu.Lock()
	if len(t.segs) == 0 {
		t.segs = []*Segment{{Start: 0, End: unknownSizeEnd}}
	}
	s := t.segs[0]
	s.Pos = 0
	info := t.info
	t.mu.Unlock()
	if err := f.Truncate(0); err != nil {
		return err
	}

	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := time.AfterFunc(e.IdleTimeout, cancel)
	defer idle.Stop()

	req, err := e.newRequest(actx, info, pr.FinalURL)
	if err != nil {
		return err
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return HTTPError{resp.StatusCode}
	}
	buf := make([]byte, defaultBufferBytes)
	var pos int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			idle.Reset(e.IdleTimeout)
			if err := e.Limiter.Wait(ctx, n); err != nil {
				return err
			}
			if err := tl.Wait(ctx, n); err != nil {
				return err
			}
			if _, err := f.WriteAt(buf[:n], pos); err != nil {
				return err
			}
			pos += int64(n)
			t.mu.Lock()
			s.Pos = pos
			t.mu.Unlock()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if actx.Err() != nil && ctx.Err() == nil {
				return fmt.Errorf("connection idle for %s", e.IdleTimeout)
			}
			return rerr
		}
	}
	if pr.Size > 0 && pos != pr.Size {
		return io.ErrUnexpectedEOF
	}
	t.mu.Lock()
	s.End = pos - 1
	t.info.Size = pos
	if pos == 0 {
		t.segs = nil
	}
	t.mu.Unlock()
	return nil
}

func verifyChecksum(path, spec string) error {
	algo, want, ok := strings.Cut(spec, ":")
	if !ok {
		algo, want = "sha256", spec
	}
	var h hash.Hash
	switch strings.ToLower(algo) {
	case "sha256":
		h = sha256.New()
	case "sha1":
		h = sha1.New()
	case "md5":
		h = md5.New()
	default:
		return fmt.Errorf("unsupported checksum %q", algo)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), strings.TrimSpace(want)) {
		return ErrChecksum
	}
	return nil
}
