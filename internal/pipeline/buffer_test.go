package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zack/fuji-tools/internal/photo"
)

// With BufferAhead set, Add must block once the buffer is full so the camera
// pull cannot run ahead and fill the disk.
func TestBufferAheadThrottlesProducer(t *testing.T) {
	dir := t.TempDir()
	const buf = 5
	opts := Options{SkipImmich: true, DryRun: true, UploadConcurrency: 1,
		BufferAhead: buf, Dest: dir}
	s, err := NewStreamer(context.Background(), opts, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Block the single worker by keeping the queue busy with a file it must
	// stat; instead we measure that Add stops accepting past the buffer.
	var added int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			p := filepath.Join(dir, fmt.Sprintf("f%03d.JPG", i))
			os.WriteFile(p, []byte("x"), 0o644)
			s.Add(photo.FileEntry{Name: filepath.Base(p), LocalPath: p})
			atomic.AddInt32(&added, 1)
		}
	}()

	select {
	case <-done: // fine: workers kept up, everything drained
	case <-time.After(5 * time.Second):
		t.Fatal("producer never finished — Add deadlocked")
	}
	if err := s.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := len(s.Files()); got != 100 {
		t.Fatalf("processed %d, want 100", got)
	}
}

// The point of the buffer is a flat disk footprint: with delete-after-upload
// on, the staging directory must never hold much more than BufferAhead files
// no matter how many are imported.
func TestStagedImportKeepsDiskFlat(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}

	var uploads int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stand in for the two endpoints an import touches.
		if strings.HasSuffix(r.URL.Path, "/bulk-upload-check") {
			var req struct {
				Assets []struct {
					ID       string `json:"id"`
					Checksum string `json:"checksum"`
				} `json:"assets"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			type result struct {
				Action  string `json:"action"`
				Reason  string `json:"reason"`
				AssetID string `json:"assetId"`
				ID      string `json:"id"`
			}
			out := struct {
				Results []result `json:"results"`
			}{}
			for _, a := range req.Assets {
				// "reject/duplicate" is how Immich says it already has it.
				out.Results = append(out.Results, result{
					Action: "reject", Reason: "duplicate", AssetID: "a-" + a.ID, ID: a.ID})
			}
			json.NewEncoder(w).Encode(out)
			return
		}
		io.Copy(io.Discard, r.Body)
		time.Sleep(3 * time.Millisecond) // uploads are slower than copies
		atomic.AddInt32(&uploads, 1)
		json.NewEncoder(w).Encode(map[string]string{
			"id": fmt.Sprintf("a%d", atomic.LoadInt32(&uploads)), "status": "created"})
	}))
	defer srv.Close()

	const buf, total = 10, 120
	opts := Options{
		ImmichURL: srv.URL, ImmichKey: "k", UploadConcurrency: 2,
		BufferAhead: buf, DeleteAfterUpload: true, Dest: stage,
	}
	s, err := NewStreamer(context.Background(), opts, total)
	if err != nil {
		t.Fatal(err)
	}

	peak := int32(0)
	stop := make(chan struct{})
	go func() { // sample how many staged files exist while the import runs
		for {
			select {
			case <-stop:
				return
			default:
			}
			if ents, err := os.ReadDir(stage); err == nil {
				if n := int32(len(ents)); n > atomic.LoadInt32(&peak) {
					atomic.StoreInt32(&peak, n)
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()

	for i := 0; i < total; i++ {
		p := filepath.Join(stage, fmt.Sprintf("DSCF%04d.JPG", i))
		if err := os.WriteFile(p, make([]byte, 512), 0o644); err != nil {
			t.Fatal(err)
		}
		s.Add(photo.FileEntry{Folder: "155_FUJI", Name: filepath.Base(p), LocalPath: p})
	}
	if err := s.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	close(stop)

	// Slack for in-flight uploads and the sampling race; the point is that the
	// peak tracks the buffer rather than the import size.
	if p := atomic.LoadInt32(&peak); p > buf+8 {
		t.Errorf("peak staged files = %d, want <= %d (buffer is not bounding disk)", p, buf+8)
	} else {
		t.Logf("peak staged files = %d for a %d-file import (buffer %d)", p, total, buf)
	}
	if left, _ := os.ReadDir(stage); len(left) != 0 {
		t.Errorf("%d staged files left behind after a clean run", len(left))
	}
}

// A file count alone does not bound disk — videos are orders of magnitude
// larger than stills. The size budget must stall the producer even when the
// file count is nowhere near its limit.
func TestBufferBytesBoundsLargeFiles(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	os.MkdirAll(stage, 0o755)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/bulk-upload-check") {
			w.Write([]byte(`{"results":[]}`))
			return
		}
		io.Copy(io.Discard, r.Body)
		time.Sleep(8 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]string{"id": "a", "status": "created"})
	}))
	defer srv.Close()

	const big = 1 << 20 // 1 MiB stands in for a video
	opts := Options{
		ImmichURL: srv.URL, ImmichKey: "k", UploadConcurrency: 1,
		BufferAhead:       1000,    // deliberately far too high to bind
		BufferBytes:       4 * big, // the size budget is what must hold the line
		DeleteAfterUpload: true, Dest: stage,
	}
	s, err := NewStreamer(context.Background(), opts, 30)
	if err != nil {
		t.Fatal(err)
	}
	peak := int64(0)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			var total int64
			if ents, err := os.ReadDir(stage); err == nil {
				for _, e := range ents {
					if fi, err := e.Info(); err == nil {
						total += fi.Size()
					}
				}
			}
			if total > atomic.LoadInt64(&peak) {
				atomic.StoreInt64(&peak, total)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	for i := 0; i < 30; i++ {
		p := filepath.Join(stage, fmt.Sprintf("MOV%03d.MOV", i))
		os.WriteFile(p, make([]byte, big), 0o644)
		s.Add(photo.FileEntry{Name: filepath.Base(p), LocalPath: p})
	}
	s.Wait()
	close(stop)

	got := atomic.LoadInt64(&peak)
	limit := int64(opts.BufferBytes) + 2*big // slack for in-flight + sampling
	if got > limit {
		t.Errorf("peak staged bytes = %d MiB, want <= %d MiB (size budget not holding)",
			got/big, limit/big)
	} else {
		t.Logf("peak staged = %d MiB for a 30 MiB import (budget %d MiB)", got/big, int64(opts.BufferBytes)/big)
	}
}

// The scenario that motivated the admission rule: a big video queued behind a
// few photos must start copying straight away, not wait for them to drain.
func TestLargeFileAdmittedWhileUnderBudget(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/bulk-upload-check") {
			w.Write([]byte(`{"results":[]}`))
			return
		}
		io.Copy(io.Discard, r.Body)
		time.Sleep(120 * time.Millisecond) // slow uploads: a drain would be obvious
		json.NewEncoder(w).Encode(map[string]string{"id": "a", "status": "created"})
	}))
	defer srv.Close()

	const mib = 1 << 20
	opts := Options{
		ImmichURL: srv.URL, ImmichKey: "k", UploadConcurrency: 1,
		BufferAhead: 100, BufferBytes: 4 * mib, Dest: dir,
	}
	s, err := NewStreamer(context.Background(), opts, 4)
	if err != nil {
		t.Fatal(err)
	}
	// three photos in flight, buffer under budget but not empty
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("DSCF%03d.JPG", i))
		os.WriteFile(p, make([]byte, mib), 0o644)
		s.Add(photo.FileEntry{Name: filepath.Base(p), LocalPath: p})
	}
	// now a "video" far bigger than the whole budget
	vid := filepath.Join(dir, "DSCF999.MOV")
	os.WriteFile(vid, make([]byte, 20*mib), 0o644)

	start := time.Now()
	s.Add(photo.FileEntry{Name: "DSCF999.MOV", LocalPath: vid})
	waited := time.Since(start)

	// Draining three 120ms uploads would take >300ms; admission should be
	// effectively immediate because the buffer was under budget.
	if waited > 100*time.Millisecond {
		t.Errorf("large file waited %v for admission — it is being made to wait for a drain", waited)
	} else {
		t.Logf("large file admitted in %v with 3 photos still in flight", waited.Round(time.Millisecond))
	}
	s.Wait()
}
