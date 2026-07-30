package cull

import (
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"

	xdraw "golang.org/x/image/draw"

	"github.com/zack/fuji-tools/internal/photo"
)

// Previews exist because a full frame is the wrong unit for browsing. A 26 MP
// JPEG is ~16 MB: about half a second of LAN transfer plus a ~0.2 s decode
// before a swipe can land, which no amount of buffering ahead can hide — the
// buffer just fills slower than a finger moves. Scaled to what a screen can
// actually show it costs ~4x less, so the viewer buffers far deeper per byte
// and decodes in a fraction of the time.
//
// Fidelity is preserved where it matters: previews are sized *above* native
// screen resolution, so fit-to-screen is pixel-for-pixel, and zooming loads
// the original frame (see the iOS viewer) — critical-focus checks still look
// at real pixels.

// previewSizes bounds what a client can ask for. An arbitrary ?max= would let
// a stray query make the host encode — and cache — unbounded variants.
var previewSizes = []int{1600, 2400, 3200, 4096}

// previewSize snaps a requested long edge up to the smallest allowed size that
// covers it, or 0 when nothing does (caller serves the original).
func previewSize(want int) int {
	for _, s := range previewSizes {
		if want <= s {
			return s
		}
	}
	return 0
}

func (p *Prefetcher) previewDir() string { return filepath.Join(p.cache, "previews") }

func (p *Prefetcher) previewPath(s *photo.Shot, size int) string {
	return filepath.Join(p.previewDir(), fmt.Sprintf("%s-%d.jpg", s.SafeID(), size))
}

// CachedPreview returns an already-generated preview path, or "" if there
// isn't one. A hit lets the server answer without waiting on the full frame.
func (p *Prefetcher) CachedPreview(s *photo.Shot, size int) string {
	path := p.previewPath(s, size)
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		return path
	}
	return ""
}

// previewJob collapses concurrent requests for the same preview onto one
// encode — without it, prefetching a burst would decode the same frame N times.
type previewJob struct {
	once sync.Once
	err  error
}

var previewJobs sync.Map // preview path -> *previewJob

// PreviewPath returns a downscaled copy of the shot's buffered full image at
// `size` px on the long edge, generating and caching it on first use.
func (p *Prefetcher) PreviewPath(s *photo.Shot, srcPath string, size int) (string, error) {
	if hit := p.CachedPreview(s, size); hit != "" {
		return hit, nil
	}
	dst := p.previewPath(s, size)
	v, _ := previewJobs.LoadOrStore(dst, &previewJob{})
	job := v.(*previewJob)
	job.once.Do(func() {
		job.err = p.generatePreview(s, srcPath, dst, size)
		previewJobs.Delete(dst)
	})
	if job.err != nil {
		return "", job.err
	}
	return dst, nil
}

func (p *Prefetcher) generatePreview(s *photo.Shot, srcPath, dst string, size int) error {
	if err := os.MkdirAll(p.previewDir(), 0o755); err != nil {
		return err
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	src, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return err
	}

	// We've paid for the decode; scoring the focus off it is nearly free.
	p.scoreDecoded(s, src)

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w >= h {
		if w > size {
			h, w = h*size/w, size
		}
	} else if h > size {
		w, h = w*size/h, size
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	// ApproxBiLinear, like the thumbnailer: CatmullRom on a 26 MP source costs
	// over a second per frame, which would just move the stall to the host.
	xdraw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), src, b, xdraw.Over, nil)
	// jpeg.Encode writes no EXIF, so bake orientation into the pixels —
	// otherwise every portrait frame would come back sideways.
	up := normalizeRGBA(scaled, p.OrientOf(s.ID))

	tmp := dst + ".gen"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	// q92 rather than the thumbnailer's 82: this is what you judge a photo on,
	// and the extra fidelity costs ~0.2 MB against a ~10 MB original.
	if err := jpeg.Encode(out, up, &jpeg.Options{Quality: 92}); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// removePreviews drops a shot's cached previews, so they age out with the full
// frame they were derived from instead of growing without bound.
func (p *Prefetcher) removePreviews(s *photo.Shot) {
	for _, size := range previewSizes {
		_ = os.Remove(p.previewPath(s, size))
	}
}
