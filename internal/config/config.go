// Package config loads and saves GoDM settings.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Schedule starts/stops the download queue at times of day ("HH:MM").
type Schedule struct {
	StartEnabled bool   `json:"startEnabled"`
	Start        string `json:"start"`
	StopEnabled  bool   `json:"stopEnabled"`
	Stop         string `json:"stop"`
	Daily        bool   `json:"daily"`
	WhenDone     string `json:"whenDone"` // "", "exit" or "shutdown"
}

// Login is a saved site login, matched by host name.
type Login struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Config struct {
	DownloadDir    string           `json:"downloadDir"`
	Categorize     bool             `json:"categorize"`
	MaxConcurrent  int              `json:"maxConcurrent"`
	Connections    int              `json:"connections"`
	SpeedLimit     int64            `json:"speedLimit"` // bytes/s, 0 = unlimited
	AutoRetry      bool             `json:"autoRetry"`
	RetryInterval  int              `json:"retryIntervalSeconds"`
	MaxAutoRetries int              `json:"maxAutoRetries"`
	Port           int              `json:"port"`
	Token          string           `json:"token"`
	Logins         map[string]Login `json:"logins,omitempty"`

	// Desktop app preferences
	MonitorClipboard   bool `json:"monitorClipboard"`
	ShowCompleteDialog bool `json:"showCompleteDialog"`
	ShowBrowserDialog  bool `json:"showBrowserDialog"`
	CloseToTray        bool `json:"closeToTray"`

	// Theme: "dark" (default, IDM-like) or "light"
	Theme string `json:"theme"`
	// Language: "en" (default), "fr", "ar"
	Language string `json:"language"`

	Schedule Schedule `json:"schedule"`

	mu   sync.Mutex
	path string
}

// Home returns the folder holding config.json and downloads.json.
func Home() string {
	if h := os.Getenv("GODM_HOME"); h != "" {
		return h
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "godm")
	}
	return ".godm"
}

func defaultDownloadDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "Downloads", "GoDM")
	}
	return "downloads"
}

func NewToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func Default() *Config {
	return &Config{
		DownloadDir:    defaultDownloadDir(),
		Categorize:     true,
		MaxConcurrent:  3,
		Connections:    8,
		AutoRetry:      true,
		RetryInterval:  30,
		MaxAutoRetries: 20,
		Port:           9614,
		Token:          NewToken(),

		MonitorClipboard:   true,
		ShowCompleteDialog: true,
		ShowBrowserDialog:  true,
		CloseToTray:        true,

		Theme:    "dark",
		Language: "en",
	}
}

// Load reads dir/config.json, creating it with defaults if missing.
func Load(dir string) (*Config, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	c := Default()
	c.path = filepath.Join(dir, "config.json")
	b, err := os.ReadFile(c.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return c, c.Save()
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if c.Token == "" {
		c.Token = NewToken()
		return c, c.Save()
	}
	return c, nil
}

func (c *Config) Path() string { return c.path }

func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(c.path, b, 0o600)
}

// Update changes settings under the config lock.
func (c *Config) Update(fn func(*Config)) error {
	c.mu.Lock()
	fn(c)
	c.mu.Unlock()
	return c.Save()
}

// LoginFor returns a saved login for the URL's host, if any.
func (c *Config) LoginFor(rawURL string) (Login, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Login{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	host := strings.ToLower(u.Hostname())
	for h, l := range c.Logins {
		h = strings.ToLower(h)
		if host == h || strings.HasSuffix(host, "."+h) {
			return l, true
		}
	}
	return Login{}, false
}

// WriteFileAtomic writes to a temp file then renames it into place.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
