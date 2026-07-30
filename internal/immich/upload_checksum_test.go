package immich

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zack/fuji-tools/internal/photo"
)

// The upload must hand Immich the checksum we computed locally, so the server
// verifies the bytes it received rather than trusting the transfer.
func TestUploadSendsChecksumHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DSCF0001.JPG")
	body := []byte("not really a jpeg, but bytes are bytes")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum(body)
	wantB64 := base64.StdEncoding.EncodeToString(sum[:])

	var gotHeader string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("x-immich-checksum")
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			if f, _, err := r.FormFile("assetData"); err == nil {
				gotBody, _ = io.ReadAll(f)
				f.Close()
			}
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "asset-1", "status": "created"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key")
	f := &photo.FileEntry{
		Folder: "155_FUJI", Name: "DSCF0001.JPG", LocalPath: path,
		SHA1: "irrelevant-hex", SHA1B64: wantB64,
	}
	if _, _, err := c.Upload(context.Background(), f); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if gotHeader != wantB64 {
		t.Errorf("x-immich-checksum = %q, want %q", gotHeader, wantB64)
	}
	// The checksum is only meaningful if it describes the bytes actually sent.
	if got := sha1.Sum(gotBody); base64.StdEncoding.EncodeToString(got[:]) != wantB64 {
		t.Errorf("uploaded bytes do not match the checksum we advertised")
	}
}

// No hash, no header — never send a checksum that doesn't describe the file.
func TestUploadOmitsEmptyChecksum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DSCF0002.JPG")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	seen := "unset"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, ok := r.Header["X-Immich-Checksum"]; ok {
			seen = v[0]
		} else {
			seen = ""
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "a", "status": "created"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key")
	f := &photo.FileEntry{Folder: "f", Name: "DSCF0002.JPG", LocalPath: path}
	if _, _, err := c.Upload(context.Background(), f); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if seen != "" {
		t.Errorf("sent checksum header %q for an unhashed file", seen)
	}
}
