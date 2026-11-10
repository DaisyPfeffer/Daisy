package gui

import (
	"embed"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Languages are JSON files: {"name": "Français", "rtl": false, "strings": {"English text": "translation"}}.
// The built-in ones live in lang/*.json (embedded). Anyone can add another language by dropping a
// <code>.json file (for example de.json or tr.json) into the "lang" folder next to GoDM.exe or in
// the GoDM settings folder. lang/en.json holds every text GoDM can show, so it is the template.

//go:embed lang/*.json
var langFS embed.FS

// Language is one translation.
type Language struct {
	Code    string
	Name    string // shown in the language list, written in the language itself
	RTL     bool   // right-to-left script (Arabic, Hebrew, ...)
	strings map[string]string
}

type langFile struct {
	Name    string            `json:"name"`
	RTL     bool              `json:"rtl"`
	Strings map[string]string `json:"strings"`
}

var (
	langMu  sync.RWMutex
	langs   = map[string]*Language{}
	current = &Language{Code: "en", Name: "English"}
)

func init() {
	langs["en"] = current
	entries, _ := langFS.ReadDir("lang")
	for _, e := range entries {
		if b, err := langFS.ReadFile("lang/" + e.Name()); err == nil {
			addLanguage(strings.TrimSuffix(e.Name(), ".json"), b)
		}
	}
	SetLanguage("en")
}

func addLanguage(code string, data []byte) bool {
	var f langFile
	if err := json.Unmarshal(data, &f); err != nil {
		return false
	}
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || strings.HasPrefix(code, "_") {
		return false
	}
	langMu.Lock()
	defer langMu.Unlock()
	l := langs[code]
	if l == nil {
		l = &Language{Code: code, strings: map[string]string{}}
		langs[code] = l
	}
	if f.Name != "" {
		l.Name = f.Name
	}
	if l.Name == "" {
		l.Name = code
	}
	l.RTL = l.RTL || f.RTL
	if l.strings == nil {
		l.strings = map[string]string{}
	}
	for k, v := range f.Strings {
		if v != "" {
			l.strings[k] = v
		}
	}
	return true
}

// LoadLanguageDir adds (or extends) languages from every *.json file in dir.
func LoadLanguageDir(dir string) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		base := filepath.Base(f)
		if b, err := os.ReadFile(f); err == nil {
			addLanguage(strings.TrimSuffix(base, filepath.Ext(base)), b)
		}
	}
}

// ExportTemplate writes the English text list so a new translation can be made from it.
func ExportTemplate(path string) error {
	b, err := langFS.ReadFile("lang/en.json")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Languages lists every language, English first.
func Languages() []Language {
	langMu.RLock()
	defer langMu.RUnlock()
	out := make([]Language, 0, len(langs))
	for _, l := range langs {
		out = append(out, Language{Code: l.Code, Name: l.Name, RTL: l.RTL})
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Code == "en") != (out[j].Code == "en") {
			return out[i].Code == "en"
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// SetLanguage switches the language; unknown codes fall back to English.
func SetLanguage(code string) {
	code = strings.ToLower(strings.TrimSpace(code))
	langMu.Lock()
	defer langMu.Unlock()
	if l, ok := langs[code]; ok {
		current = l
	} else {
		current = langs["en"]
	}
	unitRepl = nil
}

// CurrentLanguage returns the active language code.
func CurrentLanguage() string {
	langMu.RLock()
	defer langMu.RUnlock()
	return current.Code
}

// IsRTL reports whether the active language is written right to left.
func IsRTL() bool {
	langMu.RLock()
	defer langMu.RUnlock()
	return current.RTL
}

// T translates a text; texts without a translation stay English.
func T(s string) string {
	langMu.RLock()
	defer langMu.RUnlock()
	if current.strings == nil {
		return s
	}
	if t, ok := current.strings[s]; ok {
		return t
	}
	return s
}

// N marks a text for translation without translating it now (used where the
// English text is kept and translated later, e.g. menus).
func N(s string) string { return s }

var unitRepl *strings.Replacer

// Localize translates the words inside list cells such as "Complete", "Scheduled 02:00",
// "3 min 12 sec" and "1.20 MB/sec". Numbers are never touched.
func Localize(s string) string {
	if s == "" || CurrentLanguage() == "en" {
		return s
	}
	for _, p := range []string{"Scheduled", "Retry"} {
		if strings.HasPrefix(s, p+" ") {
			return T(p) + s[len(p):]
		}
	}
	if t := T(s); t != s {
		return t
	}
	langMu.Lock()
	if unitRepl == nil {
		get := func(k string) string {
			if current.strings != nil {
				if v, ok := current.strings[k]; ok {
					return v
				}
			}
			return k
		}
		unitRepl = strings.NewReplacer(
			"KB/sec", get("KB/sec"), "MB/sec", get("MB/sec"), " Bytes", " "+get("Bytes"),
			" hr", " "+get("hr"), " min", " "+get("min"), " sec", " "+get("sec"))
	}
	r := unitRepl
	langMu.Unlock()
	return r.Replace(s)
}
