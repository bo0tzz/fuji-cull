package cull

import (
	"encoding/json"
	"image"
	"log"
	"os"
	"path/filepath"
	"time"

	xdraw "golang.org/x/image/draw"

	"github.com/zack/fuji-tools/internal/photo"
)

// Per-shot sharpness store. Culling a burst means picking the one frame in
// twelve that actually nailed focus, which is the slowest part of the job by
// eye. A variance-of-Laplacian score does it at a glance.
//
// Scores are only meaningful RELATIVE to each other, and only if every score
// was measured the same way: the same operator on the same pixel scale. A
// score taken from a 240 px thumbnail and one from a full frame are not
// comparable at all — downscaling destroys exactly the high-frequency detail
// being measured. So every score is computed at one canonical working size
// (sharpWorkPx), whatever the source, and a score derived any other way is
// simply not stored.

// sharpWorkPx is the long edge every measurement is normalised to. Big enough
// that fine detail survives, small enough that the Laplacian pass is cheap
// next to the JPEG decode that precedes it.
const sharpWorkPx = 1600

func (p *Prefetcher) sharpPath() string {
	return filepath.Join(p.cache, "sharpness.json")
}

func (p *Prefetcher) loadSharp() {
	raw, err := os.ReadFile(p.sharpPath())
	if err != nil {
		return
	}
	m := map[string]float64{}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	// Hold the lock even though this runs at startup: it is the only thing
	// keeping a future reordering from racing a sweep goroutine.
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, v := range m {
		if v > 0 && p.cat.Get(id) != nil {
			p.sharp[id] = v
		}
	}
	if len(p.sharp) > 0 {
		log.Printf("sharpness: %d shots scored (persisted)", len(p.sharp))
	}
}

// sharpFlusher persists the store shortly after new scores arrive, so a sweep
// of a folder doesn't rewrite the whole JSON per shot.
func (p *Prefetcher) sharpFlusher() {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for range tick.C {
		p.mu.Lock()
		closed := p.closed
		var raw, rawTaken []byte
		if p.sharpDirty {
			raw, _ = json.Marshal(p.sharp)
			p.sharpDirty = false
		}
		if p.takenDirty {
			rawTaken, _ = json.Marshal(p.taken)
			p.takenDirty = false
		}
		p.mu.Unlock()
		if raw != nil {
			tmp := p.sharpPath() + ".tmp"
			if os.WriteFile(tmp, raw, 0o644) == nil {
				_ = os.Rename(tmp, p.sharpPath())
			}
		}
		if rawTaken != nil {
			tmp := p.takenPath() + ".tmp"
			if os.WriteFile(tmp, rawTaken, 0o644) == nil {
				_ = os.Rename(tmp, p.takenPath())
			}
		}
		if closed {
			return
		}
	}
}

// burstGap is the largest interval between consecutive frames still treated as
// one burst. Continuous shooting on the X-H2S runs many frames per second; a
// couple of seconds is loose enough to hold a burst together and tight enough
// to keep unrelated scenes apart.
const burstGap = 2

// captureUnix parses EXIF "2006:01:02 15:04:05" (camera-local, no zone) into a
// comparable seconds value. Only differences are ever used, so treating it as
// UTC is harmless.
func captureUnix(s string) int64 {
	t, err := time.Parse("2006:01:02 15:04:05", s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

func (p *Prefetcher) takenPath() string {
	return filepath.Join(p.cache, "taken.json")
}

func (p *Prefetcher) loadTaken() {
	raw, err := os.ReadFile(p.takenPath())
	if err != nil {
		return
	}
	m := map[string]int64{}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, v := range m {
		if v > 0 && p.cat.Get(id) != nil {
			p.taken[id] = v
		}
	}
}

// BurstBest returns the shots that are the sharpest frame of their burst.
//
// This is the only comparison worth surfacing. A variance-of-Laplacian score
// is dominated by how much texture a scene contains, so ranking a sequin
// close-up against a street scene says nothing about focus — but ranking the
// frames of one burst against each other says exactly what you want to know:
// which of these near-identical shots to keep. A shot is only ever compared
// with frames captured within burstGap of it, and a burst of one is not
// marked at all (there'd be nothing to have beaten).
func (p *Prefetcher) BurstBest() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	shots := p.cat.Shots
	i := 0
	for i < len(shots) {
		if shots[i].Kind != "photo" {
			i++
			continue
		}
		// extend a run of frames whose capture times are within burstGap
		j := i
		for j+1 < len(shots) && shots[j+1].Kind == "photo" {
			a, b := p.taken[shots[j].ID], p.taken[shots[j+1].ID]
			if a == 0 || b == 0 || b-a > burstGap || b < a {
				break
			}
			j++
		}
		if j > i { // a real burst: pick its best scored frame
			bestID, bestScore, scored := "", 0.0, 0
			for k := i; k <= j; k++ {
				if v, ok := p.sharp[shots[k].ID]; ok {
					scored++
					if v > bestScore {
						bestScore, bestID = v, shots[k].ID
					}
				}
			}
			// need at least two measured frames for "best" to mean anything
			if scored >= 2 && bestID != "" {
				out = append(out, bestID)
			}
		}
		i = j + 1
	}
	return out
}

// SharpnessOf returns a shot's score, or 0 when it hasn't been measured.
func (p *Prefetcher) SharpnessOf(id string) float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sharp[id]
}

// Sharpness returns every known score. Only scored shots appear, so the map
// grows as the sweep progresses and clients can merge it incrementally.
func (p *Prefetcher) Sharpness() map[string]float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]float64, len(p.sharp))
	for k, v := range p.sharp {
		out[k] = v
	}
	return out
}

func (p *Prefetcher) noteSharp(id string, score float64) {
	if score <= 0 {
		return
	}
	p.mu.Lock()
	p.sharp[id] = score
	p.sharpDirty = true
	p.mu.Unlock()
}

// scoreDecoded measures an already-decoded frame. Callers that had to decode
// anyway (preview generation, local thumbnail generation) get their score for
// the price of a downscale.
func (p *Prefetcher) scoreDecoded(s *photo.Shot, src image.Image) {
	if s.Kind != "photo" {
		return
	}
	p.mu.Lock()
	_, known := p.sharp[s.ID]
	p.mu.Unlock()
	if known {
		return
	}
	p.noteSharp(s.ID, sharpnessOf(src))
}

// sharpnessOf normalises to the canonical working size and returns the
// variance of the Laplacian — the standard focus measure. Higher is sharper.
func sharpnessOf(src image.Image) float64 {
	g := grayAtWorkScale(src)
	b := g.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 3 || h < 3 {
		return 0
	}
	// 4-neighbour Laplacian; mean and variance in one pass over the interior.
	var sum, sumSq float64
	n := 0
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			c := int(g.Pix[y*g.Stride+x])
			lap := float64(4*c -
				int(g.Pix[y*g.Stride+(x-1)]) -
				int(g.Pix[y*g.Stride+(x+1)]) -
				int(g.Pix[(y-1)*g.Stride+x]) -
				int(g.Pix[(y+1)*g.Stride+x]))
			sum += lap
			sumSq += lap * lap
			n++
		}
	}
	if n == 0 {
		return 0
	}
	mean := sum / float64(n)
	return sumSq/float64(n) - mean*mean
}

// grayAtWorkScale downscales to sharpWorkPx on the long edge and converts to
// 8-bit gray, so every score is measured on comparable pixels.
func grayAtWorkScale(src image.Image) *image.Gray {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w >= h {
		if w > sharpWorkPx {
			h, w = h*sharpWorkPx/w, sharpWorkPx
		}
	} else if h > sharpWorkPx {
		w, h = w*sharpWorkPx/h, sharpWorkPx
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewGray(image.Rect(0, 0, w, h))
	// ApproxBiLinear rather than a box filter: same choice the thumbnailer and
	// preview generator make, so a score never depends on which path produced
	// the pixels it was measured from.
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}

// sharpSweep scores buffered frames that have none yet, nearest the cursor
// first. It only ever reads files already on disk — no camera traffic — and
// yields between shots so a full-frame decode never competes with navigation.
func (p *Prefetcher) sharpSweep() {
	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}
		s := p.pickSharpTargetLocked()
		p.mu.Unlock()
		if s == nil {
			continue
		}
		f, err := os.Open(p.displayPath(s))
		if err != nil {
			continue
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			continue
		}
		p.noteSharp(s.ID, sharpnessOf(img))
	}
}

// sharpScanSpan bounds how far from the cursor the sweep looks. Only buffered
// frames qualify and buffering is a window around the cursor, so scanning the
// whole 24k catalog under p.mu — the prefetcher's hot lock — every tick would
// be pure contention for no extra coverage.
const sharpScanSpan = 400

// pickSharpTargetLocked returns the nearest buffered, unscored photo to the
// cursor, or nil. Caller holds p.mu.
func (p *Prefetcher) pickSharpTargetLocked() *photo.Shot {
	n := len(p.cat.Shots)
	for d := 0; d < sharpScanSpan; d++ {
		for _, i := range [2]int{p.cursor + d, p.cursor - d} {
			if i < 0 || i >= n {
				continue
			}
			s := p.cat.Shots[i]
			if s.Kind != "photo" {
				continue
			}
			if _, done := p.sharp[s.ID]; done {
				continue
			}
			if st, ok := p.state[s.ID]; !ok || st.Status != "ready" {
				continue // only frames already on disk
			}
			return s
		}
	}
	return nil
}
