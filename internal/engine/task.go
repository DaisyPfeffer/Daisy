package engine

import (
	"sync"
	"time"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusActive    Status = "downloading"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusError     Status = "error"
)

// Request describes what to download and how.
type Request struct {
	URL         string            `json:"url"`
	Referer     string            `json:"referer,omitempty"`
	Cookie      string            `json:"cookie,omitempty"`
	UserAgent   string            `json:"userAgent,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Username    string            `json:"username,omitempty"`
	Password    string            `json:"password,omitempty"`
	Dir         string            `json:"dir,omitempty"`
	FileName    string            `json:"fileName,omitempty"`
	Connections int               `json:"connections,omitempty"`
	SpeedLimit  int64             `json:"speedLimit,omitempty"` // bytes/s for this download, 0 = unlimited
	Checksum    string            `json:"checksum,omitempty"`   // "sha256:<hex>", "sha1:<hex>" or "md5:<hex>"
	Description string            `json:"description,omitempty"`
}

// Segment is a byte range [Start, End] (inclusive). Pos is the next byte to fetch.
type Segment struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Pos   int64 `json:"pos"`
}

func (s Segment) done() int64 {
	p := s.Pos
	if p < s.Start {
		p = s.Start
	}
	if p > s.End+1 {
		p = s.End + 1
	}
	return p - s.Start
}

// Info is a point-in-time copy of a task, safe to serialise.
type Info struct {
	ID string `json:"id"`
	Request
	Status       Status    `json:"status"`
	Error        string    `json:"error,omitempty"`
	Size         int64     `json:"size"` // -1 when unknown
	Downloaded   int64     `json:"downloaded"`
	Speed        int64     `json:"speed"`
	Resumable    bool      `json:"resumable"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"lastModified,omitempty"`
	FinalURL     string    `json:"finalUrl,omitempty"`
	Path         string    `json:"path,omitempty"`
	Segments     []Segment `json:"segments,omitempty"`
	Retries      int       `json:"retries"`
	CreatedAt    time.Time `json:"createdAt"`
	CompletedAt  time.Time `json:"completedAt,omitempty"`
	StartAt      time.Time `json:"startAt,omitempty"` // scheduler: do not start before
	RetryAt      time.Time `json:"retryAt,omitempty"`
	LastTry      time.Time `json:"lastTry,omitempty"`
}

// Public strips credentials before sending a task to a client.
func (i Info) Public() Info {
	i.Cookie, i.Password = "", ""
	i.Headers = nil
	return i
}

// Task is a download shared between the engine and the manager.
type Task struct {
	mu   sync.Mutex
	info Info
	segs []*Segment
}

func NewTask(id string, req Request) *Task {
	return &Task{info: Info{ID: id, Request: req, Status: StatusQueued, Size: -1, CreatedAt: time.Now()}}
}

// TaskFromInfo restores a task saved with Snapshot.
func TaskFromInfo(i Info) *Task {
	t := &Task{info: i}
	for _, s := range i.Segments {
		s := s
		t.segs = append(t.segs, &s)
	}
	t.info.Segments = nil
	return t
}

func (t *Task) ID() string { return t.info.ID }

func (t *Task) Snapshot() Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	i := t.info
	if i.Headers != nil {
		h := make(map[string]string, len(i.Headers))
		for k, v := range i.Headers {
			h[k] = v
		}
		i.Headers = h
	}
	i.Segments = make([]Segment, 0, len(t.segs))
	var done int64
	for _, s := range t.segs {
		i.Segments = append(i.Segments, *s)
		done += s.done()
	}
	if len(t.segs) > 0 || i.Status != StatusCompleted {
		i.Downloaded = done
	}
	return i
}

func (t *Task) Update(fn func(*Info)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fn(&t.info)
}

// pending returns segments that still have bytes left.
func (t *Task) pending() []*Segment {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*Segment
	for _, s := range t.segs {
		if s.Pos <= s.End {
			out = append(out, s)
		}
	}
	return out
}

func (t *Task) complete() bool { return len(t.pending()) == 0 }

// steal splits the largest unfinished segment in two and returns the new
// second half, so a free connection can help finish a slow one.
func (t *Task) steal(minSplit int64) *Segment {
	t.mu.Lock()
	defer t.mu.Unlock()
	var best *Segment
	var bestLeft int64
	for _, s := range t.segs {
		if left := s.End - s.Pos + 1; left > bestLeft {
			best, bestLeft = s, left
		}
	}
	if best == nil || bestLeft < 2*minSplit {
		return nil
	}
	mid := best.Pos + bestLeft/2
	ns := &Segment{Start: mid, End: best.End, Pos: mid}
	best.End = mid - 1
	t.segs = append(t.segs, ns)
	return ns
}
