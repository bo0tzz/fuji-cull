package main

import (
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"

	"github.com/zack/fuji-tools/internal/turbo"
)

// Focus peaking for the desktop viewer: a toggleable overlay painting the
// in-focus edges of the frame on screen, so "did this one nail focus, and on
// the right thing?" is a glance instead of a zoom-and-hunt.
//
// The GUI already holds every frame as decoded RGBA (turbo.Image) on its way to
// an SDL texture, so this is a pass over pixels we've already paid for — no
// engine round-trip, and it measures the FULL-resolution frame, which is what
// makes it trustworthy for critical focus.

// peakTint is the overlay colour: saturated red, which reads far more clearly
// against real photo content than the cyan this started as. It does sit near
// the reject hue, but peaking is a transient overlay you switch on deliberately
// rather than a persistent per-shot mark, so the two never share a surface.
var peakTint = struct{ R, G, B byte }{0xFF, 0x18, 0x18}

// peakCutPct is how strong an edge must be to light up, as a percentage of the
// maximum possible Sobel response. Tuned so a sharp frame shows structure and a
// soft one stays nearly bare — the contrast between the two is the whole point.
// Integer percent rather than a float fraction: the threshold is compared
// against integer magnitudes, so there's no reason to leave the int domain.
const peakCutPct = 14

// edgeOverlay returns a transparent RGBA image with the frame's strong edges
// painted in peakTint, ready to upload as a texture and blend over the photo.
func edgeOverlay(img *turbo.Image) *turbo.Image {
	w, h := img.W, img.H
	out := &turbo.Image{Pix: make([]byte, w*h*4), W: w, H: h}
	if w < 3 || h < 3 {
		return out
	}
	// Luminance plane first: Sobel on grey is one pass instead of three, and
	// focus is a luminance property anyway.
	lum := make([]byte, w*h)
	for i, p := 0, 0; i < w*h; i, p = i+1, p+4 {
		// integer Rec.601 — the fractional precision buys nothing here
		lum[i] = byte((uint32(img.Pix[p])*299 + uint32(img.Pix[p+1])*587 + uint32(img.Pix[p+2])*114) / 1000)
	}

	// Sobel magnitude, approximated as |gx|+|gy| (cheaper than the hypot and
	// indistinguishable once thresholded).
	const maxResp = 4 * 255 // |gx|+|gy| ceiling for this kernel pair
	cut := maxResp * peakCutPct / 100
	for y := 1; y < h-1; y++ {
		row := y * w
		up, dn := row-w, row+w
		for x := 1; x < w-1; x++ {
			tl, tc, tr := int(lum[up+x-1]), int(lum[up+x]), int(lum[up+x+1])
			ml, mr := int(lum[row+x-1]), int(lum[row+x+1])
			bl, bc, br := int(lum[dn+x-1]), int(lum[dn+x]), int(lum[dn+x+1])
			gx := (tr + 2*mr + br) - (tl + 2*ml + bl)
			gy := (bl + 2*bc + br) - (tl + 2*tc + tr)
			if gx < 0 {
				gx = -gx
			}
			if gy < 0 {
				gy = -gy
			}
			mag := gx + gy
			if mag < cut {
				continue
			}
			// Ramp alpha above the cut so the strongest edges read hardest,
			// rather than everything past the threshold looking identical.
			a := (mag - cut) * 255 / (maxResp - cut)
			if a > 255 {
				a = 255
			}
			o := (row + x) * 4
			out.Pix[o] = peakTint.R
			out.Pix[o+1] = peakTint.G
			out.Pix[o+2] = peakTint.B
			out.Pix[o+3] = byte(a)
		}
	}
	return out
}

// uploadOverlay uploads an edge overlay as an alpha-blended texture. Unlike
// uploadRGBA (which is BLENDMODE_NONE for opaque frames) this must blend, or it
// would paint an opaque black rectangle over the photo.
func uploadOverlay(r *sdl.Renderer, img *turbo.Image) (*texEntry, error) {
	tex, err := r.CreateTexture(uint32(sdl.PIXELFORMAT_ABGR8888), sdl.TEXTUREACCESS_STATIC,
		int32(img.W), int32(img.H))
	if err != nil {
		return nil, err
	}
	if err := tex.Update(nil, unsafe.Pointer(&img.Pix[0]), img.W*4); err != nil {
		tex.Destroy()
		return nil, err
	}
	tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	return &texEntry{tex: tex, w: int32(img.W), h: int32(img.H)}, nil
}

// peakTex returns the cached peaking overlay for a shot, building it on first
// use from the frame the decode pool already holds. Nil when the frame isn't
// decoded yet — the caller simply draws no overlay that redraw.
func (u *ui) peakTex(id string) *texEntry {
	if te := u.peaks.get(id); te != nil {
		return te
	}
	d := u.pool.Get(id)
	if d == nil || d.img == nil || d.err != nil {
		return nil
	}
	te, err := uploadOverlay(u.ren, edgeOverlay(d.img))
	if err != nil {
		return nil
	}
	u.peaks.put(id, te)
	return te
}
