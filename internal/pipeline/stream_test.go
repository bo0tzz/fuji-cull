package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/zack/fuji-tools/internal/photo"
)

// The streamer is fed while the camera copy is still running, so it must
// accept files one at a time, process every one, and never wedge the producer.
func TestStreamerProcessesEveryFile(t *testing.T) {
	dir := t.TempDir()
	const n = 40
	var want []photo.FileEntry
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("DSCF%04d.JPG", i))
		if err := os.WriteFile(p, make([]byte, 1024+i), 0o644); err != nil {
			t.Fatal(err)
		}
		want = append(want, photo.FileEntry{Folder: "155_FUJI", Name: filepath.Base(p), LocalPath: p})
	}

	// SkipImmich + DryRun: exercises the plumbing with no server and no
	// external tools, which is the part that can deadlock or drop files.
	opts := Options{SkipImmich: true, DryRun: true, UploadConcurrency: 4, Dest: dir}
	s, err := NewStreamer(context.Background(), opts, n)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range want {
		s.Add(f)
	}
	if err := s.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	got := s.Files()
	if len(got) != n {
		t.Fatalf("processed %d files, want %d", len(got), n)
	}
	for i, f := range got {
		if f.Size != int64(1024+i) {
			t.Errorf("%s: Size = %d, want %d (worker did not stat it)", f.Name, f.Size, 1024+i)
		}
	}
}

// Add is called from the copy loop; a slow or blocked consumer must not stall
// it beyond the queue, and concurrent producers must not corrupt the list.
func TestStreamerConcurrentAdd(t *testing.T) {
	dir := t.TempDir()
	const n = 100
	opts := Options{SkipImmich: true, DryRun: true, UploadConcurrency: 2, Dest: dir}
	s, err := NewStreamer(context.Background(), opts, n)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%03d.JPG", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			s.Add(photo.FileEntry{Name: filepath.Base(p), LocalPath: p})
		}(p)
	}
	wg.Wait()
	if err := s.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := len(s.Files()); got != n {
		t.Fatalf("processed %d, want %d", got, n)
	}
}

// A file that vanishes between copy and hash must surface as an error rather
// than being silently skipped — in upload-only mode that error is the only
// thing standing between a missing upload and a deleted local copy.
func TestStreamerReportsMissingFile(t *testing.T) {
	dir := t.TempDir()
	opts := Options{SkipImmich: true, DryRun: true, UploadConcurrency: 1, Dest: dir}
	s, err := NewStreamer(context.Background(), opts, 1)
	if err != nil {
		t.Fatal(err)
	}
	s.Add(photo.FileEntry{Name: "gone.JPG", LocalPath: filepath.Join(dir, "gone.JPG")})
	if err := s.Wait(); err == nil {
		t.Fatal("Wait returned nil for a file that does not exist")
	}
}
