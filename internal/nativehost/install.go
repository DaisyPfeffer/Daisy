package nativehost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	"godm/internal/config"
)

// ChromeExtensionID is the fixed ID of the bundled extension (from the
// "key" in extension/manifest.json), so it can be registered automatically.
const ChromeExtensionID = "bfbiolljappphhedpmdijggcihjkemgb"

// FirefoxExtensionID matches browser_specific_settings.gecko.id.
const FirefoxExtensionID = "godm@godm.local"

type target struct {
	dir    string
	regKey string // Windows only: registry key that points at the manifest
}

func targets(firefox bool) []target {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		base := filepath.Join(config.Home(), "native-host")
		if firefox {
			return []target{{filepath.Join(base, "firefox"), `Software\Mozilla\NativeMessagingHosts\` + Name}}
		}
		dir := filepath.Join(base, "chrome")
		return []target{
			{dir, `Software\Google\Chrome\NativeMessagingHosts\` + Name},
			{dir, `Software\Microsoft\Edge\NativeMessagingHosts\` + Name},
			{dir, `Software\BraveSoftware\Brave-Browser\NativeMessagingHosts\` + Name},
			{dir, `Software\Chromium\NativeMessagingHosts\` + Name},
		}
	case "darwin":
		as := filepath.Join(home, "Library", "Application Support")
		if firefox {
			return []target{{filepath.Join(as, "Mozilla", "NativeMessagingHosts"), ""}}
		}
		return []target{
			{filepath.Join(as, "Google", "Chrome", "NativeMessagingHosts"), ""},
			{filepath.Join(as, "Chromium", "NativeMessagingHosts"), ""},
			{filepath.Join(as, "Microsoft Edge", "NativeMessagingHosts"), ""},
			{filepath.Join(as, "BraveSoftware", "Brave-Browser", "NativeMessagingHosts"), ""},
		}
	default:
		if firefox {
			return []target{{filepath.Join(home, ".mozilla", "native-messaging-hosts"), ""}}
		}
		c := filepath.Join(home, ".config")
		return []target{
			{filepath.Join(c, "google-chrome", "NativeMessagingHosts"), ""},
			{filepath.Join(c, "chromium", "NativeMessagingHosts"), ""},
			{filepath.Join(c, "microsoft-edge", "NativeMessagingHosts"), ""},
			{filepath.Join(c, "BraveSoftware", "Brave-Browser", "NativeMessagingHosts"), ""},
		}
	}
}

// Install registers hostExe (the console "godm" program) as the native
// messaging host for the given Chrome extension IDs and, optionally, Firefox.
// It returns the files or registry keys it wrote.
func Install(hostExe string, chromeIDs []string, firefox bool) ([]string, error) {
	var done []string
	install := func(ts []target, manifest map[string]any) error {
		b, _ := json.MarshalIndent(manifest, "", "  ")
		for _, t := range ts {
			if err := os.MkdirAll(t.dir, 0o755); err != nil {
				return err
			}
			p := filepath.Join(t.dir, Name+".json")
			if err := os.WriteFile(p, b, 0o644); err != nil {
				return err
			}
			if t.regKey != "" {
				if err := registerManifest(t.regKey, p); err != nil {
					return err
				}
				done = append(done, `HKCU\`+t.regKey)
			} else {
				done = append(done, p)
			}
		}
		return nil
	}
	if len(chromeIDs) > 0 {
		if err := install(targets(false), Manifest(hostExe, chromeIDs, nil)); err != nil {
			return done, err
		}
	}
	if firefox {
		if err := install(targets(true), Manifest(hostExe, nil, []string{FirefoxExtensionID})); err != nil {
			return done, err
		}
	}
	return done, nil
}
