package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"godm/internal/config"
	"godm/internal/engine"
	"godm/internal/manager"
)

func setup(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	home := t.TempDir()
	cfg, _ := config.Load(home)
	cfg.DownloadDir = home + "/dl"
	m, err := manager.New(cfg, home, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	ts := httptest.NewServer(New(m, "tok123").Handler())
	t.Cleanup(ts.Close)
	return ts, "tok123"
}

func call(t *testing.T, method, url, token string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	if token != "" {
		req.Header.Set("X-GoDM-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestAPIRequiresToken(t *testing.T) {
	ts, tok := setup(t)
	for _, tc := range []struct{ tok string }{{""}, {"wrong"}} {
		if r, _ := call(t, "GET", ts.URL+"/api/downloads", tc.tok, nil); r.StatusCode != 401 {
			t.Errorf("token %q: status %d, want 401", tc.tok, r.StatusCode)
		}
	}
	if r, _ := call(t, "POST", ts.URL+"/api/downloads", "", AddRequest{}); r.StatusCode != 401 {
		t.Errorf("POST without token: %d, want 401", r.StatusCode)
	}
	if r, _ := call(t, "GET", ts.URL+"/api/downloads", tok, nil); r.StatusCode != 200 {
		t.Errorf("valid token: %d, want 200", r.StatusCode)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/downloads", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 200 {
		t.Errorf("bearer token: %d, want 200", r.StatusCode)
	}
}

func TestDashboardIsServed(t *testing.T) {
	ts, _ := setup(t)
	r, b := call(t, "GET", ts.URL+"/", "", nil)
	if r.StatusCode != 200 || !bytes.Contains(b, []byte("GoDM")) {
		t.Fatalf("dashboard status %d", r.StatusCode)
	}
}

func TestAddListPauseResumeRemoveAndSecretsHidden(t *testing.T) {
	data := bytes.Repeat([]byte("z"), 2000)
	file := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "z.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer file.Close()
	ts, tok := setup(t)

	r, b := call(t, "POST", ts.URL+"/api/downloads", tok, AddRequest{
		Request: engine.Request{URL: file.URL + "/z.bin", Cookie: "secret=1", Username: "u", Password: "p"}, Paused: true})
	if r.StatusCode != 201 {
		t.Fatalf("add: %d %s", r.StatusCode, b)
	}
	var added []engine.Info
	json.Unmarshal(b, &added)
	if len(added) != 1 || added[0].Status != engine.StatusPaused {
		t.Fatalf("added = %+v", added)
	}
	if bytes.Contains(b, []byte("secret=1")) || bytes.Contains(b, []byte(`"p"`)) {
		t.Fatal("credentials leaked in add response")
	}
	id := added[0].ID

	_, b = call(t, "GET", ts.URL+"/api/downloads", tok, nil)
	if bytes.Contains(b, []byte("secret=1")) || bytes.Contains(b, []byte("password")) {
		t.Fatal("credentials leaked in list")
	}
	if r, _ := call(t, "POST", ts.URL+"/api/downloads/"+id+"/resume", tok, nil); r.StatusCode != 200 {
		t.Fatalf("resume: %d", r.StatusCode)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		_, b = call(t, "GET", ts.URL+"/api/downloads/"+id, tok, nil)
		var i engine.Info
		json.Unmarshal(b, &i)
		if i.Status == engine.StatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not completed: %s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if r, _ := call(t, "DELETE", ts.URL+"/api/downloads/"+id, tok, nil); r.StatusCode != 200 {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	if r, _ := call(t, "GET", ts.URL+"/api/downloads/"+id, tok, nil); r.StatusCode != 404 {
		t.Fatalf("after delete: %d, want 404", r.StatusCode)
	}
}

func TestAddValidation(t *testing.T) {
	ts, tok := setup(t)
	for name, body := range map[string]any{
		"no url":         AddRequest{},
		"file scheme":    AddRequest{Request: engine.Request{URL: "file:///etc/passwd"}},
		"too many conns": AddRequest{Request: engine.Request{URL: "http://127.0.0.1:1/x", Connections: 99}},
	} {
		if r, _ := call(t, "POST", ts.URL+"/api/downloads", tok, body); r.StatusCode != 400 {
			t.Errorf("%s: status %d, want 400", name, r.StatusCode)
		}
	}
}

func TestSettingsValidation(t *testing.T) {
	ts, tok := setup(t)
	bad := 0
	if r, _ := call(t, "PUT", ts.URL+"/api/settings", tok, manager.Settings{MaxConcurrent: &bad}); r.StatusCode != 400 {
		t.Errorf("maxConcurrent 0 accepted: %d", r.StatusCode)
	}
	lim := int64(512 * 1024)
	r, b := call(t, "PUT", ts.URL+"/api/settings", tok, manager.Settings{SpeedLimit: &lim})
	if r.StatusCode != 200 {
		t.Fatalf("put settings: %d %s", r.StatusCode, b)
	}
	var got map[string]any
	json.Unmarshal(b, &got)
	if int64(got["speedLimit"].(float64)) != lim {
		t.Fatalf("speedLimit = %v", got["speedLimit"])
	}
}
