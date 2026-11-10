//go:build windows

package nativehost

import "golang.org/x/sys/windows/registry"

func registerManifest(key, manifestPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("", manifestPath)
}
