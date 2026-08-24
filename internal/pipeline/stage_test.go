package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zack/fuji-tools/internal/immich"
	"github.com/zack/fuji-tools/internal/photo"
)

// The stack lane's denominator is complete RAF+JPG pairs, not files and not
// shots. An import carrying JPG-only shots or videos would otherwise leave the
// bar permanently short of its own end.
func TestStackStageCountsOnlyCompletePairs(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	files := []photo.FileEntry{
		{Folder: "151_FUJI", Name: "DSCF0001.JPG", AssetID: "a1"},
		{Folder: "151_FUJI", Name: "DSCF0001.RAF", AssetID: "a2"},
		{Folder: "151_FUJI", Name: "DSCF0002.JPG", AssetID: "a3"},
		{Folder: "151_FUJI", Name: "DSCF0002.RAF", AssetID: "a4"},
		{Folder: "151_FUJI", Name: "DSCF0003.JPG", AssetID: "a5"}, // JPG only
		{Folder: "151_FUJI", Name: "DSCF0004.MOV", AssetID: "a6"}, // video
	}

	var last Stage
	var sawTotals []int
	opts := Options{StageProgress: func(name string, st Stage) {
		if name != StageStack {
			t.Fatalf("unexpected stage %q", name)
		}
		last = st
		sawTotals = append(sawTotals, st.FilesTotal)
	}}
	StackPairs(context.Background(), opts, immich.NewClient(srv.URL, "k"), files)

	if posts != 2 {
		t.Errorf("CreateStack calls = %d, want 2", posts)
	}
	if last.FilesTotal != 2 {
		t.Errorf("FilesTotal = %d, want 2 (complete pairs only)", last.FilesTotal)
	}
	if last.Files != 2 {
		t.Errorf("Files = %d, want 2", last.Files)
	}
	if last.Failed != 0 {
		t.Errorf("Failed = %d, want 0", last.Failed)
	}
	if !last.Done {
		t.Error("final stack stage should be Done")
	}
	for i, tot := range sawTotals {
		if tot != 2 {
			t.Errorf("update %d reported FilesTotal %d, want a stable 2", i, tot)
		}
	}
}

// With no pairs at all the lane must still resolve, or a JPG-only import ends
// with a stack lane that never leaves "running".
func TestStackStageResolvesWithNoPairs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("CreateStack must not be called when there are no pairs")
	}))
	defer srv.Close()

	var last Stage
	var updates int
	opts := Options{StageProgress: func(_ string, st Stage) { last, updates = st, updates+1 }}
	StackPairs(context.Background(), opts, immich.NewClient(srv.URL, "k"),
		[]photo.FileEntry{{Folder: "151_FUJI", Name: "DSCF0001.JPG", AssetID: "a1"}})

	if updates == 0 {
		t.Fatal("no stage update reported")
	}
	if !last.Done || last.FilesTotal != 0 {
		t.Errorf("got %+v, want Done with FilesTotal 0", last)
	}
}

// A failed upload must never read as progress: it belongs in Failed, and Files
// must stay at the count the server actually accepted. Before this, a run where
// every upload failed finished with a full bar.
func TestUploadStageKeepsFailuresOutOfProgress(t *testing.T) {
	var got Stage
	s := &Streamer{
		total:    10,
		opts:     Options{TotalBytes: 1000, StageProgress: func(_ string, st Stage) { got = st }},
		inflight: map[string]*fileProg{},
		ok:       3,
		dup:      1,
		fail:     2,
		upBytes:  400,
	}
	s.emitUpload()

	if got.Files != 4 {
		t.Errorf("Files = %d, want 4 (accepted: ok+dup)", got.Files)
	}
	if got.Failed != 2 {
		t.Errorf("Failed = %d, want 2", got.Failed)
	}
	if got.FilesTotal != 10 || got.BytesTotal != 1000 {
		t.Errorf("denominators = %d/%d, want 10/1000", got.FilesTotal, got.BytesTotal)
	}
	if got.Bytes != 400 {
		t.Errorf("Bytes = %d, want 400", got.Bytes)
	}
}
