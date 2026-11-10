// Package manager runs the download queue: concurrency limits, scheduling,
// automatic retries, speed tracking and persistence.
package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"godm/internal/config"
	"godm/internal/engine"
)

var (
	errPaused   = errors.New("paused")
	errRemoved  = errors.New("removed")
	errShutdown = errors.New("shutting down")
	ErrNotFound = errors.New("download not found")
)

type entry struct {
	task    *engine.Task
	ctx     context.Context
	cancel  context.CancelCauseFunc
	running bool
	removed bool
	delFile bool
	lastB   int64
	lastT   time.Time
}

type Manager struct {
	cfg       *config.Config
	eng       *engine.Engine
	storePath string
	log       *log.Logger

	mu      sync.Mutex
	entries map[string]*entry
	wg      sync.WaitGroup
	stop    chan struct{}
	done    chan struct{}
	dirty   bool
}

func New(cfg *config.Config, home string, logger *log.Logger) (*Manager, error) {
	if logger == nil {
		logger = log.New(os.Stderr, "godm ", log.LstdFlags)
	}
	eng := engine.New()
	m := &Manager{
		cfg:       cfg,
		eng:       eng,
		storePath: filepath.Join(home, "downloads.json"),
		log:       logger,
		entries:   map[string]*entry{},
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	m.applySettings()
	if err := m.load(); err != nil {
		return nil, err
	}
	go m.loop()
	return m, nil
}

// Engine exposes the engine so tests can tune timeouts.
func (m *Manager) Engine() *engine.Engine { return m.eng }

func (m *Manager) applySettings() {
	m.eng.DefaultDir = m.cfg.DownloadDir
	m.eng.Categorize = m.cfg.Categorize
	m.eng.Connections = m.cfg.Connections
	if m.eng.Limiter == nil {
		m.eng.Limiter = engine.NewLimiter(m.cfg.SpeedLimit)
	} else {
		m.eng.Limiter.SetRate(m.cfg.SpeedLimit)
	}
}

// Settings is the subset of config editable at runtime.
type Settings struct {
	DownloadDir   *string `json:"downloadDir,omitempty"`
	MaxConcurrent *int    `json:"maxConcurrent,omitempty"`
	Connections   *int    `json:"connections,omitempty"`
	SpeedLimit    *int64  `json:"speedLimit,omitempty"`
	AutoRetry     *bool   `json:"autoRetry,omitempty"`
	Categorize    *bool   `json:"categorize,omitempty"`
}

func (m *Manager) UpdateSettings(s Settings) error {
	if s.MaxConcurrent != nil && (*s.MaxConcurrent < 1 || *s.MaxConcurrent > 32) {
		return errors.New("maxConcurrent must be 1-32")
	}
	if s.Connections != nil && (*s.Connections < 1 || *s.Connections > 32) {
		return errors.New("connections must be 1-32")
	}
	if s.SpeedLimit != nil && *s.SpeedLimit < 0 {
		return errors.New("speedLimit must be >= 0")
	}
	err := m.cfg.Update(func(c *config.Config) {
		if s.DownloadDir != nil && *s.DownloadDir != "" {
			c.DownloadDir = *s.DownloadDir
		}
		if s.MaxConcurrent != nil {
			c.MaxConcurrent = *s.MaxConcurrent
		}
		if s.Connections != nil {
			c.Connections = *s.Connections
		}
		if s.SpeedLimit != nil {
			c.SpeedLimit = *s.SpeedLimit
		}
		if s.AutoRetry != nil {
			c.AutoRetry = *s.AutoRetry
		}
		if s.Categorize != nil {
			c.Categorize = *s.Categorize
		}
	})
	m.mu.Lock()
	m.applySettings()
	m.mu.Unlock()
	return err
}

func (m *Manager) Config() *config.Config { return m.cfg }

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// AddOptions control how a new download is queued.
type AddOptions struct {
	Paused  bool
	StartAt time.Time
}

func (m *Manager) Add(req engine.Request, opt AddOptions) (engine.Info, error) {
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return engine.Info{}, fmt.Errorf("invalid URL %q: only http and https are supported", req.URL)
	}
	if req.Connections < 0 || req.Connections > 32 {
		return engine.Info{}, errors.New("connections must be 1-32")
	}
	if req.Username == "" && u.User != nil {
		req.Username = u.User.Username()
		req.Password, _ = u.User.Password()
		u.User = nil
		req.URL = u.String()
	}
	if req.Username == "" {
		if l, ok := m.cfg.LoginFor(req.URL); ok {
			req.Username, req.Password = l.Username, l.Password
		}
	}
	req.FileName = engine.SanitizeFileName(req.FileName)

	t := engine.NewTask(newID(), req)
	t.Update(func(i *engine.Info) {
		i.StartAt = opt.StartAt
		if opt.Paused {
			i.Status = engine.StatusPaused
		}
	})
	m.mu.Lock()
	m.entries[t.ID()] = &entry{task: t}
	m.dirty = true
	m.mu.Unlock()
	m.kick()
	return t.Snapshot(), nil
}

// List returns all downloads, oldest first.
func (m *Manager) List() []engine.Info {
	m.mu.Lock()
	out := make([]engine.Info, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e.task.Snapshot())
	}
	m.mu.Unlock()
	sort.Slice(out, func(a, b int) bool {
		if out[a].CreatedAt.Equal(out[b].CreatedAt) {
			return out[a].ID < out[b].ID
		}
		return out[a].CreatedAt.Before(out[b].CreatedAt)
	})
	return out
}

func (m *Manager) Get(id string) (engine.Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return engine.Info{}, ErrNotFound
	}
	return e.task.Snapshot(), nil
}

func (m *Manager) Pause(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return ErrNotFound
	}
	if e.running {
		e.cancel(errPaused)
		return nil
	}
	st := e.task.Snapshot().Status
	if st == engine.StatusQueued || st == engine.StatusError {
		e.task.Update(func(i *engine.Info) { i.Status = engine.StatusPaused; i.RetryAt = time.Time{} })
		m.dirty = true
	}
	return nil
}

func (m *Manager) Resume(id string) error {
	m.mu.Lock()
	e, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if !e.running {
		e.task.Update(func(i *engine.Info) {
			if i.Status != engine.StatusCompleted {
				i.Status = engine.StatusQueued
				i.Error, i.RetryAt, i.Retries = "", time.Time{}, 0
				i.StartAt = time.Time{}
			}
		})
		m.dirty = true
	}
	m.mu.Unlock()
	m.kick()
	return nil
}

func (m *Manager) PauseAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.entries))
	for id := range m.entries {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Pause(id)
	}
}

func (m *Manager) ResumeAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.entries))
	for id := range m.entries {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Resume(id)
	}
}

// Remove deletes a download. Partial data is always deleted; the finished
// file only when deleteFile is true.
func (m *Manager) Remove(id string, deleteFile bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return ErrNotFound
	}
	delete(m.entries, id)
	m.dirty = true
	e.removed, e.delFile = true, deleteFile
	if e.running {
		e.cancel(errRemoved) // cleanup happens when the worker exits
		return nil
	}
	cleanup(e.task.Snapshot(), deleteFile)
	return nil
}

// ErrStarted is returned by Edit once a download has begun writing its file.
var ErrStarted = errors.New("download already started; its file name and folder can no longer change")

// Edit changes a download's settings (folder, name, description, ...). The
// save location can only change before the download has created its file.
func (m *Manager) Edit(id string, fn func(*engine.Request)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return ErrNotFound
	}
	if e.running {
		return ErrStarted
	}
	var err error
	e.task.Update(func(i *engine.Info) {
		before := i.Request
		fn(&i.Request)
		if i.Path != "" && (i.Request.Dir != before.Dir || i.Request.FileName != before.FileName) {
			i.Request.Dir, i.Request.FileName = before.Dir, before.FileName
			err = ErrStarted
		}
		i.Request.FileName = engine.SanitizeFileName(i.Request.FileName)
	})
	m.dirty = true
	return err
}

// RemoveCompleted removes every finished download from the list (files stay).
func (m *Manager) RemoveCompleted() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, e := range m.entries {
		if !e.running && e.task.Snapshot().Status == engine.StatusCompleted {
			delete(m.entries, id)
			n++
		}
	}
	if n > 0 {
		m.dirty = true
	}
	return n
}

func cleanup(i engine.Info, deleteFile bool) {
	if i.Path == "" {
		return
	}
	os.Remove(i.Path + engine.PartSuffix)
	if deleteFile && i.Status == engine.StatusCompleted {
		os.Remove(i.Path)
	}
}

func (m *Manager) kick() {
	go func() {
		m.mu.Lock()
		m.schedule(time.Now())
		m.mu.Unlock()
	}()
}

// schedule starts queued downloads up to the concurrency limit. m.mu held.
func (m *Manager) schedule(now time.Time) {
	running := 0
	var queued []*entry
	for _, e := range m.entries {
		if e.running {
			running++
			continue
		}
		i := e.task.Snapshot()
		switch i.Status {
		case engine.StatusError:
			if !i.RetryAt.IsZero() && !now.Before(i.RetryAt) {
				e.task.Update(func(i *engine.Info) { i.Status = engine.StatusQueued; i.RetryAt = time.Time{} })
				queued = append(queued, e)
			}
		case engine.StatusQueued:
			if i.StartAt.IsZero() || !now.Before(i.StartAt) {
				queued = append(queued, e)
			}
		}
	}
	sort.Slice(queued, func(a, b int) bool {
		return queued[a].task.Snapshot().CreatedAt.Before(queued[b].task.Snapshot().CreatedAt)
	})
	for _, e := range queued {
		if running >= m.cfg.MaxConcurrent {
			break
		}
		m.start(e)
		running++
	}
}

func (m *Manager) start(e *entry) {
	ctx, cancel := context.WithCancelCause(context.Background())
	e.ctx, e.cancel, e.running = ctx, cancel, true
	e.lastB, e.lastT = e.task.Snapshot().Downloaded, time.Now()
	e.task.Update(func(i *engine.Info) {
		i.Status = engine.StatusActive
		i.Error = ""
		i.Speed = 0
		i.LastTry = time.Now()
	})
	m.dirty = true
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		err := m.eng.Run(ctx, e.task)
		m.finish(e, err)
	}()
}

func (m *Manager) finish(e *entry, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.running = false
	cause := context.Cause(e.ctx)
	e.cancel(nil)
	m.dirty = true
	if e.removed {
		cleanup(e.task.Snapshot(), e.delFile)
		return
	}
	now := time.Now()
	e.task.Update(func(i *engine.Info) {
		i.Speed = 0
		switch {
		case err == nil:
			i.Status, i.CompletedAt, i.Error = engine.StatusCompleted, now, ""
			i.Downloaded = i.Size
		case errors.Is(cause, errPaused):
			i.Status = engine.StatusPaused
		case errors.Is(cause, errShutdown):
			i.Status = engine.StatusQueued
		default:
			i.Status, i.Error = engine.StatusError, err.Error()
			i.Retries++
			if m.cfg.AutoRetry && !engine.IsPermanent(err) && i.Retries <= m.cfg.MaxAutoRetries {
				i.RetryAt = now.Add(time.Duration(m.cfg.RetryInterval) * time.Second)
			}
		}
	})
	if err != nil && cause == nil {
		m.log.Printf("download %s failed: %v", e.task.ID(), err)
	}
	go func() { // a slot freed up
		m.mu.Lock()
		m.schedule(time.Now())
		m.mu.Unlock()
	}()
}

func (m *Manager) loop() {
	defer close(m.done)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	saveEvery := 0
	for {
		select {
		case <-m.stop:
			return
		case now := <-tick.C:
			m.mu.Lock()
			active := false
			for _, e := range m.entries {
				if !e.running {
					continue
				}
				active = true
				d := e.task.Snapshot().Downloaded
				if dt := now.Sub(e.lastT).Seconds(); dt > 0 {
					inst := float64(d-e.lastB) / dt
					e.task.Update(func(i *engine.Info) {
						// smoothed speed so the number does not jump around
						i.Speed = int64(0.6*float64(i.Speed) + 0.4*inst)
					})
				}
				e.lastB, e.lastT = d, now
			}
			m.schedule(now)
			saveEvery++
			if m.dirty || (active && saveEvery%4 == 0) {
				m.saveLocked()
			}
			m.mu.Unlock()
		}
	}
}

// Close stops all downloads (they resume on next start) and saves state.
func (m *Manager) Close() error {
	close(m.stop)
	<-m.done
	m.mu.Lock()
	for _, e := range m.entries {
		if e.running {
			e.cancel(errShutdown)
		}
	}
	m.mu.Unlock()
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked()
}

func (m *Manager) saveLocked() error {
	list := make([]engine.Info, 0, len(m.entries))
	for _, e := range m.entries {
		i := e.task.Snapshot()
		i.Speed = 0
		list = append(list, i)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(m.storePath, b, 0o600); err != nil {
		m.log.Printf("save state: %v", err)
		return err
	}
	m.dirty = false
	return nil
}

func (m *Manager) load() error {
	b, err := os.ReadFile(m.storePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []engine.Info
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("read %s: %w", m.storePath, err)
	}
	for _, i := range list {
		if i.Status == engine.StatusActive {
			i.Status = engine.StatusQueued // interrupted by a crash or restart
		}
		i.Speed = 0
		t := engine.TaskFromInfo(i)
		m.entries[t.ID()] = &entry{task: t}
	}
	return nil
}
