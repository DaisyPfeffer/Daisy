package nativehost

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func frame(t *testing.T, v any) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := WriteMessage(&b, v); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestServeRoundTrip(t *testing.T) {
	var in bytes.Buffer
	in.Write(frame(t, Message{Cmd: "ping"}))
	in.Write(frame(t, Message{URL: "https://example.com/a.zip", Referer: "https://example.com/"}))
	in.Write(frame(t, Message{URL: "bad"}))
	in.Write(frame(t, Message{Cmd: "nope"}))

	var out bytes.Buffer
	var seen []Message
	err := Serve(&in, &out, func(m Message) (string, error) {
		seen = append(seen, m)
		if m.URL == "bad" {
			return "", errors.New("invalid URL")
		}
		return "abc123", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var replies []Reply
	for out.Len() > 0 {
		var r Reply
		if err := ReadMessage(&out, &r); err != nil {
			t.Fatal(err)
		}
		replies = append(replies, r)
	}
	if len(replies) != 4 {
		t.Fatalf("got %d replies, want 4", len(replies))
	}
	if !replies[0].OK || replies[0].ID != "" {
		t.Errorf("ping reply = %+v", replies[0])
	}
	if !replies[1].OK || replies[1].ID != "abc123" {
		t.Errorf("download reply = %+v", replies[1])
	}
	if replies[2].OK || replies[2].Error != "invalid URL" {
		t.Errorf("error reply = %+v", replies[2])
	}
	if replies[3].OK || replies[3].Error == "" {
		t.Errorf("unknown command reply = %+v", replies[3])
	}
	if len(seen) != 2 || seen[0].Referer != "https://example.com/" {
		t.Errorf("handler saw %+v", seen)
	}
}

func TestRejectsOversizedMessage(t *testing.T) {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, uint32(MaxMessage+1))
	var m Message
	if err := ReadMessage(&b, &m); err == nil {
		t.Fatal("expected an error for an oversized message")
	}
}

func TestManifest(t *testing.T) {
	m := Manifest("/usr/bin/godm", []string{"abcdef"}, []string{"godm@godm.local"})
	if m["name"] != Name || m["type"] != "stdio" || m["path"] != "/usr/bin/godm" {
		t.Fatalf("manifest = %v", m)
	}
	if o := m["allowed_origins"].([]string); o[0] != "chrome-extension://abcdef/" {
		t.Fatalf("origins = %v", o)
	}
}
