import CoreImage
import CoreImage.CIFilterBuiltins
import UIKit

// Focus peaking: a toggleable overlay that paints the in-focus edges of the
// displayed frame, so "did this one nail focus, and on the right thing?" is a
// glance instead of a pinch-zoom hunt.
//
// Computed on-device rather than in the engine. It's a view concern — you flick
// it on and off while comparing frames — and a second network fetch per shot
// would undo the buffering work the viewer depends on. On the GPU this is a few
// milliseconds for a screen-sized frame.
//
// What it measures depends on what's on screen, and that's deliberate: at
// fit-to-screen the viewer shows a ~screen-sized preview, so peaking answers
// "is the subject broadly sharp"; zoomed past 1.2x the viewer has swapped in
// the original frame, so peaking then runs on real sensor pixels and answers
// critical focus.
enum FocusPeaking {
    /// Shared GPU context — building one per frame would dwarf the filter cost.
    private static let ctx = CIContext(options: [.useSoftwareRenderer: false])
    private static let cache = NSCache<NSString, UIImage>()

    /// Peaking colour: saturated red, which reads far more clearly against real
    /// photo content than the cyan this started as. It sits near the reject hue,
    /// but peaking is a transient overlay you switch on deliberately rather than
    /// a persistent per-shot mark, so the two never share a surface.
    private static let tint = CIColor(red: 1.0, green: 0.09, blue: 0.09)

    /// How hard an edge must be before it lights up. Higher = only the
    /// crispest detail survives, which is what makes the overlay readable
    /// rather than a wash of noise.
    static let defaultThreshold: Double = 0.55

    /// Build (or reuse) the peaking overlay for `image`. Returns nil if the
    /// filter chain fails, in which case the caller just shows no overlay.
    static func overlay(for image: UIImage, key: String,
                        threshold: Double = defaultThreshold) -> UIImage? {
        let ck = "\(key)|\(threshold)" as NSString
        if let hit = cache.object(forKey: ck) { return hit }
        guard let cg = image.cgImage else { return nil }
        let src = CIImage(cgImage: cg)

        // Sobel edge magnitude, then collapse to a single channel.
        let edges = src
            .applyingFilter("CIEdges", parameters: ["inputIntensity": 8.0])
            .applyingFilter("CIMaximumComponent")

        // Threshold: push everything below the cut toward black and everything
        // above toward white, so MaskToAlpha yields crisp lines. A contrast
        // ramp with a matching brightness offset is the cheap way to do it.
        let contrast = 12.0
        let thresholded = edges.applyingFilter("CIColorControls", parameters: [
            kCIInputContrastKey: contrast,
            kCIInputBrightnessKey: -threshold * contrast * 0.08,
            kCIInputSaturationKey: 0.0,
        ])

        // Luminance -> alpha, then paint the tint through it, over nothing.
        let mask = thresholded.applyingFilter("CIMaskToAlpha")
        let colored = CIImage(color: tint).cropped(to: src.extent)
        let out = colored.applyingFilter("CIBlendWithAlphaMask", parameters: [
            kCIInputBackgroundImageKey: CIImage.empty(),
            kCIInputMaskImageKey: mask,
        ])

        guard let rendered = ctx.createCGImage(out, from: src.extent) else { return nil }
        let ui = UIImage(cgImage: rendered, scale: image.scale, orientation: image.imageOrientation)
        // Overlays are transparent PNG-ish bitmaps of the same pixel count as
        // the frame, so budget them like the frames themselves.
        cache.setObject(ui, forKey: ck,
                        cost: Int(ui.size.width * ui.size.height * ui.scale * ui.scale * 4))
        return ui
    }

    /// Called when memory is tight, and when the user turns peaking off — no
    /// reason to hold a second full-size bitmap per frame while it's hidden.
    static func flush() { cache.removeAllObjects() }
}
