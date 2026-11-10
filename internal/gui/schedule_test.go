package gui

import (
	"testing"
	"time"

	"godm/internal/config"
	"godm/internal/engine"
)

type fakeActions struct{ log []string }

func (f *fakeActions) startQueue() { f.log = append(f.log, "start") }
func (f *fakeActions) stopQueue()  { f.log = append(f.log, "stop") }
func (f *fakeActions) exitApp()    { f.log = append(f.log, "exit") }
func (f *fakeActions) shutdownPC() { f.log = append(f.log, "shutdown") }

func TestNextAt(t *testing.T) {
	now := time.Date(2026, 11, 9, 23, 30, 0, 0, time.Local)
	if got := NextAt("23:45", now); !got.Equal(time.Date(2026, 11, 9, 23, 45, 0, 0, time.Local)) {
		t.Fatalf("later today: %v", got)
	}
	if got := NextAt("02:00", now); !got.Equal(time.Date(2026, 11, 10, 2, 0, 0, 0, time.Local)) {
		t.Fatalf("tomorrow: %v", got)
	}
	if !NextAt("25:99", now).IsZero() {
		t.Fatal("bad time must give zero")
	}
}

func TestScheduleStartsStopsAndRunsWhenDone(t *testing.T) {
	now := time.Date(2026, 11, 9, 1, 0, 0, 0, time.Local)
	var s schedule
	s.set(config.Schedule{StartEnabled: true, Start: "02:00", StopEnabled: true, Stop: "06:00", WhenDone: "shutdown"}, now)
	act := &fakeActions{}
	busy := []engine.Info{{Status: engine.StatusActive}}
	idle := []engine.Info{{Status: engine.StatusCompleted}}

	s.check(act, busy, now.Add(30*time.Minute)) // 01:30, nothing yet
	if len(act.log) != 0 {
		t.Fatalf("acted too early: %v", act.log)
	}
	at2 := time.Date(2026, 11, 9, 2, 0, 0, 0, time.Local)
	s.check(act, busy, at2)
	s.check(act, busy, at2.Add(time.Minute)) // still downloading: no shutdown
	s.check(act, idle, at2.Add(2*time.Minute))
	if len(act.log) != 2 || act.log[0] != "start" || act.log[1] != "shutdown" {
		t.Fatalf("log = %v, want [start shutdown]", act.log)
	}
	s.check(act, idle, time.Date(2026, 11, 9, 6, 0, 0, 0, time.Local))
	if act.log[len(act.log)-1] != "stop" {
		t.Fatalf("log = %v, want stop at 06:00", act.log)
	}
	if !s.nextStart.IsZero() {
		t.Fatal("one-time schedule must not re-arm")
	}
}

func TestDailyScheduleReArms(t *testing.T) {
	now := time.Date(2026, 11, 9, 1, 0, 0, 0, time.Local)
	var s schedule
	s.set(config.Schedule{StartEnabled: true, Start: "02:00", Daily: true}, now)
	act := &fakeActions{}
	s.check(act, nil, time.Date(2026, 11, 9, 2, 0, 0, 0, time.Local))
	if !s.nextStart.Equal(time.Date(2026, 11, 10, 2, 0, 0, 0, time.Local)) {
		t.Fatalf("next start = %v, want tomorrow 02:00", s.nextStart)
	}
}

func TestWaitingRetryCountsAsPending(t *testing.T) {
	if !pending([]engine.Info{{Status: engine.StatusError, RetryAt: time.Now()}}) {
		t.Fatal("download waiting for retry is still pending")
	}
	if pending([]engine.Info{{Status: engine.StatusError}, {Status: engine.StatusPaused}}) {
		t.Fatal("failed/paused downloads are not pending")
	}
}
