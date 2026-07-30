import UIKit

// FullImageStore is the full-frame viewer's image buffer — the client-side
// analogue of the engine's "150 ahead / 50 behind" disk buffer, and the
// full-image counterpart to ThumbCache.
//
// It has two tiers, because a 26 MP frame costs ~104 MB decoded but only
// ~16 MB as JPEG bytes:
//
//   near  — decoded UIImages in an NSCache, so a page renders on frame ONE
//           (no black flash, no spinner). Only a handful fit in memory.
//   far   — raw JPEG bytes on disk in the session's URLCache. Cheap, so the
//           window can be wide; a hit here skips the network entirely and
//           only pays the ~0.2s decode.
//
// The tiers are sized to match: `decodeAhead`/`decodeBehind` never exceed what
// `totalCostLimit` can actually hold, or prefetching would evict the very
// frames it just warmed — which looked exactly like "no prefetch at all".
final class FullImageStore {
    static let shared = FullImageStore()

    /// Long edge to request from the host. Sized above the screen's own
    /// resolution so fit-to-screen is still pixel-for-pixel; the engine snaps
    /// it up to its nearest cached size.
    static let previewMax: Int = {
        let b = UIScreen.main.nativeBounds
        return Int(max(b.width, b.height))
    }()

    /// Decoded frames kept ahead of / behind the viewer cursor. A preview
    /// decodes to ~27 MB against a full frame's ~104 MB, so the same memory
    /// budget holds several times more of them.
    static let decodeAhead = 4
    static let decodeBehind = 2
    /// Raw-JPEG (disk) window. Ahead-biased: culling runs forwards.
    static let warmAhead = 20
    static let warmBehind = 6
    /// Original-frame window, warmed only while the viewer is zoomed — these
    /// are ~12 MB each, so the window stays tight. Comparing a burst at one
    /// crop steps shot to shot, and each step wants real pixels on arrival.
    static let fullAhead = 2
    static let fullBehind = 1

    private let cache = NSCache<NSString, UIImage>()
    private let lock = NSLock()
    private var decoding = Set<String>()
    private var warming = Set<String>()
    /// Frames already pulled to disk. Every page change re-runs the wide sweep,
    /// so without this each swipe would re-read the whole window off disk.
    /// Bounded well under the URLCache's own capacity; if a note here outlives
    /// the cached bytes the on-demand load just re-fetches.
    private var warmed = Set<String>()
    private var warmedOrder: [String] = []
    private static let warmedCap = 300

    /// Separate lanes so the near tier is never stuck behind a dozen far-tier
    /// fetches. Their sum stays under httpMaximumConnectionsPerHost.
    private let nearGate = Gate(limit: 2)
    private let farGate = Gate(limit: 4)

    /// Full images get their own session: a big disk cache, and a connection
    /// pool the thumbnail flood can't starve (the same reasoning as the
    /// engine's control session).
    let session: URLSession

    init() {
        // Full-frame JPEGs decode large (~104 MB each at 26 MP), so budget by
        // bytes; NSCache also drops under pressure. ~4 frames — keep in step
        // with decodeAhead + decodeBehind + the current one.
        cache.totalCostLimit = 448 << 20

        let cfg = URLSessionConfiguration.default
        // URLCache refuses to store any response bigger than ~5% of its disk
        // capacity, so a 16 MB frame needs GBs of headroom to be cached at all.
        // At 256 MB (the old shared cache) full images were silently never
        // stored, and every revisit re-fetched over the network.
        cfg.urlCache = URLCache(memoryCapacity: 64 << 20,
                                diskCapacity: 6 << 30,
                                directory: nil)
        // A shot's pixels never change under its id, and the engine marks the
        // response immutable — prefer the disk copy without revalidating.
        cfg.requestCachePolicy = .returnCacheDataElseLoad
        cfg.httpMaximumConnectionsPerHost = 6
        cfg.timeoutIntervalForRequest = 30
        session = URLSession(configuration: cfg)
    }

    // MARK: - memory tier

    func image(for url: URL) -> UIImage? {
        cache.object(forKey: url.absoluteString as NSString)
    }

    /// Cache an image the viewer just decoded on-demand, so paging back to it
    /// (or re-showing after a page rebuild) is instant.
    func remember(_ img: UIImage, for url: URL) {
        let cost = Int(img.size.width * img.size.height * img.scale * img.scale * 4)
        cache.setObject(img, forKey: url.absoluteString as NSString, cost: cost)
    }

    // MARK: - loading

    /// Fetch (disk cache first, then network) and force-decode off the main
    /// thread. Returns a ready-to-draw bitmap, or nil if the fetch failed.
    func load(_ url: URL) async -> UIImage? {
        if let hit = image(for: url) { return hit }
        guard let (data, resp) = try? await session.data(from: url),
              (resp as? HTTPURLResponse)?.statusCode == 200,
              let raw = UIImage(data: data),
              // byPreparingForDisplay decodes now, off the main thread, so the
              // first draw doesn't stall (a stall reads as a flash too)
              let ready = await raw.byPreparingForDisplay() else { return nil }
        remember(ready, for: url)
        return ready
    }

    // MARK: - prefetch tiers

    /// Near tier: fetch + decode `url` so the next flick lands on a ready
    /// bitmap. No-op if it's already cached or in flight.
    func prefetch(_ url: URL) {
        let key = url.absoluteString
        if image(for: url) != nil { return }
        lock.lock()
        if decoding.contains(key) { lock.unlock(); return }
        decoding.insert(key)
        lock.unlock()

        Task.detached(priority: .utility) { [self] in
            await nearGate.acquire()
            let ok = await load(url) != nil
            await nearGate.release()
            lock.lock()
            decoding.remove(key)
            // decoded implies its bytes are on disk — the far tier can skip it
            if ok { noteWarmedLocked(key) }
            lock.unlock()
        }
    }

    /// Far tier: pull the JPEG bytes into the disk cache without decoding, so
    /// a jump into this range costs a decode instead of a LAN round-trip.
    /// This is the client's version of the engine buffering ahead of you.
    func warm(_ url: URL) {
        let key = url.absoluteString
        if image(for: url) != nil { return }
        lock.lock()
        if warmed.contains(key) || warming.contains(key) || decoding.contains(key) {
            lock.unlock()
            return
        }
        warming.insert(key)
        lock.unlock()

        Task.detached(priority: .background) { [self] in
            await farGate.acquire()
            var req = URLRequest(url: url)
            // If it's already on disk this returns without touching the network.
            req.cachePolicy = .returnCacheDataElseLoad
            let ok = (try? await session.data(for: req)) != nil
            await farGate.release()
            lock.lock()
            warming.remove(key)
            if ok { noteWarmedLocked(key) }
            lock.unlock()
        }
    }
}

extension FullImageStore {
    /// Record that `key`'s bytes are on disk, trimming the oldest notes.
    /// Caller holds `lock`.
    fileprivate func noteWarmedLocked(_ key: String) {
        guard !warmed.contains(key) else { return }
        warmed.insert(key)
        warmedOrder.append(key)
        if warmedOrder.count > Self.warmedCap {
            warmed.subtract(warmedOrder.prefix(100))
            warmedOrder.removeFirst(100)
        }
    }
}

/// Minimal async semaphore — bounds how many prefetches share the link at
/// once, without blocking a cooperative thread.
actor Gate {
    private let limit: Int
    private var active = 0
    private var waiters: [CheckedContinuation<Void, Never>] = []

    init(limit: Int) { self.limit = limit }

    func acquire() async {
        if active < limit { active += 1; return }
        await withCheckedContinuation { waiters.append($0) }
    }

    func release() {
        if waiters.isEmpty { active -= 1 } else { waiters.removeFirst().resume() }
    }
}
