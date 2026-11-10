package engine

import (
	"mime"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// SanitizeFileName makes a name safe on Windows, macOS and Linux.
func SanitizeFileName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 32, r == 127:
			return -1
		case strings.ContainsRune(`<>:"/\|?*`, r):
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if len(name) > 200 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		base := name[:200-len(ext)]
		for !utf8.ValidString(base) && len(base) > 0 {
			base = base[:len(base)-1]
		}
		name = base + ext
	}
	switch strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name))) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "LPT1", "LPT2", "LPT3":
		name = "_" + name
	}
	return name
}

// FileNameFrom picks a file name from Content-Disposition, then the URL path,
// then the content type.
func FileNameFrom(contentDisposition, rawURL, contentType string) string {
	if contentDisposition != "" {
		if _, params, err := mime.ParseMediaType(contentDisposition); err == nil {
			if n := SanitizeFileName(path.Base(strings.ReplaceAll(params["filename"], `\`, "/"))); n != "" && n != "." && n != "/" {
				return n
			}
		}
	}
	if u, err := url.Parse(rawURL); err == nil {
		p := u.Path
		if p != "" && !strings.HasSuffix(p, "/") {
			if n := SanitizeFileName(path.Base(p)); n != "" && n != "." {
				return n
			}
		}
	}
	name := "download"
	if contentType != "" {
		if mt, _, err := mime.ParseMediaType(contentType); err == nil {
			if exts, _ := mime.ExtensionsByType(mt); len(exts) > 0 {
				name += exts[0]
			}
		}
	}
	return name
}

var categories = map[string]string{
	".zip": "Compressed", ".rar": "Compressed", ".7z": "Compressed", ".gz": "Compressed",
	".tar": "Compressed", ".bz2": "Compressed", ".xz": "Compressed", ".tgz": "Compressed",
	".pdf": "Documents", ".doc": "Documents", ".docx": "Documents", ".xls": "Documents",
	".xlsx": "Documents", ".ppt": "Documents", ".pptx": "Documents", ".txt": "Documents", ".epub": "Documents",
	".mp3": "Music", ".flac": "Music", ".wav": "Music", ".aac": "Music", ".ogg": "Music", ".m4a": "Music",
	".mp4": "Video", ".mkv": "Video", ".avi": "Video", ".mov": "Video", ".webm": "Video", ".flv": "Video", ".wmv": "Video",
	".exe": "Programs", ".msi": "Programs", ".dmg": "Programs", ".deb": "Programs", ".rpm": "Programs",
	".apk": "Programs", ".iso": "Programs", ".appimage": "Programs",
}

// Category returns the sub-folder used to group a file by extension.
func Category(name string) string {
	if c, ok := categories[strings.ToLower(filepath.Ext(name))]; ok {
		return c
	}
	return "General"
}
