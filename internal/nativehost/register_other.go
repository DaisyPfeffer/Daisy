//go:build !windows

package nativehost

// Browsers on macOS and Linux find the manifest by its folder; no registry.
func registerManifest(key, manifestPath string) error { return nil }
