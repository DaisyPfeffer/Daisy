// Package nativehost implements the browser native-messaging protocol
// (4-byte little-endian length + JSON) so a browser extension can hand
// downloads to GoDM.
package nativehost

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const Name = "com.godm.host"

// MaxMessage is the largest message we accept from the browser (1 MiB,
// which matches Chrome's limit for messages sent to the browser).
const MaxMessage = 1 << 20

type Message struct {
	Cmd       string `json:"cmd"` // "download" (default) or "ping"
	URL       string `json:"url"`
	Referer   string `json:"referer,omitempty"`
	Cookie    string `json:"cookie,omitempty"`
	UserAgent string `json:"userAgent,omitempty"`
	FileName  string `json:"fileName,omitempty"`
}

type Reply struct {
	OK    bool   `json:"ok"`
	ID    string `json:"id,omitempty"`
	Error string `json:"error,omitempty"`
}

func ReadMessage(r io.Reader, v any) error {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return err
	}
	if n > MaxMessage {
		return fmt.Errorf("message too large: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return json.Unmarshal(buf, v)
}

func WriteMessage(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(len(b))); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// Serve answers messages until the browser closes stdin.
func Serve(r io.Reader, w io.Writer, add func(Message) (string, error)) error {
	for {
		var m Message
		err := ReadMessage(r, &m)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			var se *json.SyntaxError
			if errors.As(err, &se) {
				if werr := WriteMessage(w, Reply{Error: "invalid JSON"}); werr != nil {
					return werr
				}
				continue
			}
			return err
		}
		var rep Reply
		switch m.Cmd {
		case "ping":
			rep.OK = true
		case "", "download":
			id, err := add(m)
			if err != nil {
				rep.Error = err.Error()
			} else {
				rep.OK, rep.ID = true, id
			}
		default:
			rep.Error = "unknown command " + m.Cmd
		}
		if err := WriteMessage(w, rep); err != nil {
			return err
		}
	}
}

// Manifest builds the JSON manifest browsers use to find the host.
func Manifest(exePath string, chromeIDs []string, firefoxIDs []string) map[string]any {
	m := map[string]any{
		"name":        Name,
		"description": "GoDM download manager helper",
		"path":        exePath,
		"type":        "stdio",
	}
	if len(chromeIDs) > 0 {
		origins := make([]string, 0, len(chromeIDs))
		for _, id := range chromeIDs {
			origins = append(origins, "chrome-extension://"+id+"/")
		}
		m["allowed_origins"] = origins
	}
	if len(firefoxIDs) > 0 {
		m["allowed_extensions"] = firefoxIDs
	}
	return m
}
