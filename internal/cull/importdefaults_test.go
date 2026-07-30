package cull

import (
	"os"
	"testing"
)

// The settings screen writes an API key into this file, so it must round-trip
// intact, must not clobber the remembered import destination, and must not be
// world-readable.
func TestImportDefaultsRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	saveImportDefaults("/tmp/keepers", "trip-album")
	saveImmichDefaults("https://immich.example.com/", "secret-key", "trip-album", true)

	got := loadImportDefaults()
	if got.Dest != "/tmp/keepers" {
		t.Errorf("credentials clobbered the import destination: Dest = %q", got.Dest)
	}
	if got.ImmichURL != "https://immich.example.com/" || got.ImmichKey != "secret-key" {
		t.Errorf("credentials did not round-trip: %+v", got)
	}
	if !got.ImmichStack {
		t.Error("stack flag did not round-trip")
	}

	// a later import must not wipe the stored credentials
	saveImportDefaults("/tmp/other", "other-album")
	if after := loadImportDefaults(); after.ImmichKey != "secret-key" {
		t.Errorf("an import wiped the stored key: %+v", after)
	}

	fi, err := os.Stat(importDefaultsPath())
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("file holds an API key but mode is %o, want 600", mode)
	}
}
