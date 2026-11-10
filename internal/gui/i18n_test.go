package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func loadKeys(t *testing.T, file string) map[string]string {
	t.Helper()
	b, err := langFS.ReadFile("lang/" + file)
	if err != nil {
		t.Fatal(err)
	}
	var f langFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return f.Strings
}

// Every language file must translate every text of lang/en.json.
func TestLanguagesComplete(t *testing.T) {
	en := loadKeys(t, "en.json")
	entries, _ := langFS.ReadDir("lang")
	for _, e := range entries {
		if e.Name() == "en.json" {
			continue
		}
		got := loadKeys(t, e.Name())
		for k := range en {
			if got[k] == "" {
				t.Errorf("%s: missing translation for %q", e.Name(), k)
			}
		}
		for k := range got {
			if _, ok := en[k]; !ok {
				t.Errorf("%s: text not in en.json: %q", e.Name(), k)
			}
		}
	}
}

// Every T("...") in the source must be listed in en.json, or it can never be translated.
func TestSourceTextsAreListed(t *testing.T) {
	en := loadKeys(t, "en.json")
	re := regexp.MustCompile(`\bT\("((?:[^"\\]|\\.)*)"\)`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			var key string
			if err := json.Unmarshal([]byte(`"`+m[1]+`"`), &key); err != nil {
				key = m[1]
			}
			if _, ok := en[key]; !ok {
				t.Errorf("%s: T(%q) is not in lang/en.json", f, key)
			}
		}
	}
}

func TestTranslateAndLocalize(t *testing.T) {
	defer SetLanguage("en")
	SetLanguage("fr")
	if got := T("Cancel"); got != "Annuler" {
		t.Errorf("fr Cancel = %q", got)
	}
	if got := T("not a known text"); got != "not a known text" {
		t.Errorf("unknown text changed: %q", got)
	}
	if got := Localize("Complete"); got != "Terminé" {
		t.Errorf("Localize Complete = %q", got)
	}
	if got := Localize("3 min 12 sec"); got != "3 min 12 s" {
		t.Errorf("Localize duration = %q", got)
	}
	if got := Localize("1.20 MB/sec"); got != "1.20 Mo/s" {
		t.Errorf("Localize rate = %q", got)
	}
	if got := Localize("Scheduled 02:00"); got != "Planifié 02:00" {
		t.Errorf("Localize scheduled = %q", got)
	}
	SetLanguage("ar")
	if !IsRTL() {
		t.Error("Arabic must be right-to-left")
	}
	SetLanguage("xx")
	if CurrentLanguage() != "en" {
		t.Error("unknown language must fall back to English")
	}
}

func TestExternalLanguage(t *testing.T) {
	defer SetLanguage("en")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "it.json"), []byte(`{"name":"Italiano","strings":{"Cancel":"Annulla"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "_template.json"), []byte(`{"name":"Template","strings":{}}`), 0o644)
	LoadLanguageDir(dir)
	SetLanguage("it")
	if T("Cancel") != "Annulla" || T("OK") != "OK" {
		t.Error("external language not loaded")
	}
	for _, l := range Languages() {
		if l.Code == "_template" {
			t.Error("_template.json must not become a language")
		}
	}
}
