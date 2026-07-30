package cull

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Persisted desktop settings: the import destination/album an import last ran
// with, plus Immich credentials entered in the app. Explicit flags and env
// still win over anything remembered here.
//
// Desktop had no way to enter Immich credentials at all — flags and env only —
// and a .app launched from Finder inherits neither, so double-clicking the icon
// could never reach a server. This file is what the settings screen writes.
type importDefaults struct {
	Dest        string `json:"dest"`
	Album       string `json:"album"`
	ImmichURL   string `json:"immichURL,omitempty"`
	ImmichKey   string `json:"immichKey,omitempty"`
	ImmichStack bool   `json:"immichStack,omitempty"`
}

func importDefaultsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "fuji-cull", "import-defaults.json")
}

func loadImportDefaults() importDefaults {
	var d importDefaults
	raw, err := os.ReadFile(importDefaultsPath())
	if err == nil {
		_ = json.Unmarshal(raw, &d)
	}
	return d
}

// writeImportDefaults persists the whole record at 0600 — it holds an API key,
// so it must not be world-readable even though the original dest/album file
// was harmless.
func writeImportDefaults(d importDefaults) {
	raw, _ := json.Marshal(d)
	path := importDefaultsPath()
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) != nil {
		return
	}
	if os.Rename(tmp, path) != nil {
		os.Remove(tmp)
		return
	}
	// An older build wrote this file 0644 before it carried a key; tighten it.
	_ = os.Chmod(path, 0o600)
}

// saveImportDefaults records where the last import went, preserving any stored
// credentials alongside it.
func saveImportDefaults(dest, album string) {
	d := loadImportDefaults()
	d.Dest, d.Album = dest, album
	writeImportDefaults(d)
}

// saveImmichDefaults records credentials entered in the settings screen,
// preserving the remembered import destination.
func saveImmichDefaults(url, key, album string, stack bool) {
	d := loadImportDefaults()
	d.ImmichURL, d.ImmichKey, d.ImmichStack = url, key, stack
	d.Album = album
	writeImportDefaults(d)
}
