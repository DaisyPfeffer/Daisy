package gui

import (
	"fmt"
	"strings"
	"time"

	"godm/internal/config"
	"godm/internal/engine"
)

// schedActions is what the scheduler can make the app do.
type schedActions interface {
	startQueue()
	stopQueue()
	exitApp()
	shutdownPC()
}

// schedule starts/stops the queue at set times of day, like the scheduler of
// classic download managers, and can exit or shut down when the queue is done.
type schedule struct {
	cfg       config.Schedule
	nextStart time.Time
	nextStop  time.Time
	running   bool // the scheduler started the queue and waits for it to finish
	startedAt time.Time
}

// ParseClock reads "HH:MM" (24h).
func ParseClock(s string) (h, m int, err error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, 0, fmt.Errorf("time must look like 23:30")
	}
	return t.Hour(), t.Minute(), nil
}

// NextAt is the next moment after now with the given clock time.
func NextAt(clock string, now time.Time) time.Time {
	h, m, err := ParseClock(clock)
	if err != nil {
		return time.Time{}
	}
	at := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !at.After(now) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

func (s *schedule) load(c *config.Config) {
	s.set(c.Schedule, time.Now())
}

func (s *schedule) set(sc config.Schedule, now time.Time) {
	s.cfg = sc
	s.nextStart, s.nextStop = time.Time{}, time.Time{}
	if sc.StartEnabled {
		s.nextStart = NextAt(sc.Start, now)
	}
	if sc.StopEnabled {
		s.nextStop = NextAt(sc.Stop, now)
	}
}

func (s *schedule) check(act schedActions, list []engine.Info, now time.Time) {
	if !s.nextStart.IsZero() && !now.Before(s.nextStart) {
		act.startQueue()
		s.running, s.startedAt = true, now
		s.nextStart = time.Time{}
		if s.cfg.Daily {
			s.nextStart = NextAt(s.cfg.Start, now)
		}
	}
	if !s.nextStop.IsZero() && !now.Before(s.nextStop) {
		act.stopQueue()
		s.running = false
		s.nextStop = time.Time{}
		if s.cfg.Daily {
			s.nextStop = NextAt(s.cfg.Stop, now)
		}
	}
	if s.running && s.cfg.WhenDone != "" && now.Sub(s.startedAt) > 5*time.Second && !pending(list) {
		s.running = false
		switch s.cfg.WhenDone {
		case "exit":
			act.exitApp()
		case "shutdown":
			act.shutdownPC()
		}
	}
}

// pending reports whether anything is still downloading or waiting to.
func pending(list []engine.Info) bool {
	for _, i := range list {
		switch i.Status {
		case engine.StatusActive, engine.StatusQueued:
			return true
		case engine.StatusError:
			if !i.RetryAt.IsZero() {
				return true
			}
		}
	}
	return false
}

func (s *schedule) describe() string {
	var parts []string
	if !s.nextStart.IsZero() {
		parts = append(parts, "queue starts "+s.nextStart.Format("Mon 15:04"))
	}
	if !s.nextStop.IsZero() {
		parts = append(parts, "stops "+s.nextStop.Format("Mon 15:04"))
	}
	return strings.Join(parts, ", ")
}
