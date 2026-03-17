#!/usr/bin/env swift
// Borderless image viewer for TRMNL preview
// Usage: viewer <image.png> [zoom%]
// Press Q, Escape, or Cmd+W to close
//
// Single-instance: if a viewer is already running, sends it SIGUSR1 to reload
// and exits immediately. The running instance reloads from disk on SIGUSR1.
import AppKit
import CoreImage

let args  = CommandLine.arguments
guard args.count >= 2 else { print("Usage: viewer <image.png> [zoom%]"); exit(1) }

let imagePath = (args[1] as NSString).expandingTildeInPath

// Compute zoom so the image appears at the TRMNL's true physical size (8.247" wide).
//
// NSWindow sizes are in logical points, so we need logical_PPI (points per inch).
// We get it directly: screen.frame.size.width is how many logical points span the
// panel, and CGDisplayScreenSize gives the panel's physical width in mm.
//
//   logical_PPI = screen_points_wide / physical_inches_wide
//   zoom        = (8.247in × logical_PPI) / 1872px
//
// MBP 14" example: 1512 pts / 11.91in = 127 logical PPI → 8.247×127/1872 ≈ 56%
func autoZoom() -> Double {
    let trmnlWidthInches = 8.247
    let imgWidthPx       = 1872.0
    guard let screen    = NSScreen.main,
          let screenNum = screen.deviceDescription[NSDeviceDescriptionKey("NSScreenNumber")] as? NSNumber
    else { return 0.56 }

    let displayID      = screenNum.uint32Value as CGDirectDisplayID
    let physicalSizeMM = CGDisplayScreenSize(displayID)
    guard physicalSizeMM.width > 0 else { return 0.56 }

    let logicalPPI = Double(screen.frame.size.width) / (Double(physicalSizeMM.width) / 25.4)
    return (trmnlWidthInches * logicalPPI) / imgWidthPx
}

let zoom: Double
if args.count >= 3, let explicit = Double(args[2]) {
    zoom = explicit / 100.0
} else {
    zoom = autoZoom()
}

// -- Single-instance via PID file + SIGUSR1 --

let pidFile = "/tmp/trmnl-viewer.pid"

// If another instance is alive, signal it to reload and exit.
if let existing = try? String(contentsOfFile: pidFile, encoding: .utf8),
   let pid = pid_t(existing.trimmingCharacters(in: .whitespacesAndNewlines)),
   kill(pid, 0) == 0 {
    kill(pid, SIGUSR1)
    exit(0)
}

// Write our own PID.
try? String(ProcessInfo.processInfo.processIdentifier)
    .write(toFile: pidFile, atomically: true, encoding: .utf8)

// Bridge SIGUSR1 to the main run loop via a pipe (signal handlers must be
// async-signal-safe, so we just write a byte and handle it on the main queue).
var sigPipe = [Int32](repeating: 0, count: 2)
pipe(&sigPipe)

signal(SIGUSR1) { _ in
    var b: UInt8 = 1
    write(sigPipe[1], &b, 1)
}

// -- AppKit --

class KeyWindow: NSWindow {
    override var canBecomeKey: Bool { true }
    override func keyDown(with event: NSEvent) {
        let ch = event.characters?.lowercased()
        if event.keyCode == 53 ||                                   // Esc
           ch == "q" ||                                             // Q
           (event.modifierFlags.contains(.command) && ch == "w") {  // Cmd+W
            NSApp.terminate(nil)
        }
    }
}

// Simulate e-ink's limited dynamic range.
func einkSimulate(_ src: NSImage) -> NSImage {
    guard let tiff = src.tiffRepresentation,
          let ci   = CIImage(data: tiff) else { return src }

    // CIColorMatrix operates in linear light, so work in linear throughout.
    func srgbToLinear(_ c: CGFloat) -> CGFloat {
        c <= 0.04045 ? c / 12.92 : pow((c + 0.055) / 1.055, 2.4)
    }

    let contrastRatio: CGFloat = 10.0
    let hi    = srgbToLinear(180.0 / 255.0)  // e-ink white point (reflective off-white)
    let lo    = hi / contrastRatio           // e-ink black point derived from CR
    let scale = hi - lo

    // output = input * scale + lo (applied per channel via CIColorMatrix)
    let sv   = CIVector(x: scale, y: 0,  z: 0,  w: 0)
    let bias = CIVector(x: lo,    y: lo, z: lo, w: 0)

    let filter = CIFilter(name: "CIColorMatrix")!
    filter.setValue(ci,   forKey: kCIInputImageKey)
    filter.setValue(sv,   forKey: "inputRVector")
    filter.setValue(sv,   forKey: "inputGVector")
    filter.setValue(sv,   forKey: "inputBVector")
    filter.setValue(CIVector(x: 0, y: 0, z: 0, w: 1), forKey: "inputAVector")
    filter.setValue(bias, forKey: "inputBiasVector")

    guard let out = filter.outputImage else { return src }
    let ctx = CIContext()
    guard let cg = ctx.createCGImage(out, from: out.extent) else { return src }
    return NSImage(cgImage: cg, size: src.size)
}

class AppDelegate: NSObject, NSApplicationDelegate {
    var window: NSWindow!
    var imageView: NSImageView!
    var sigSource: DispatchSourceRead?

    func applicationDidFinishLaunching(_: Notification) {
        guard let image = NSImage(contentsOfFile: imagePath) else {
            fputs("viewer: cannot load \(imagePath)\n", stderr)
            NSApp.terminate(nil)
            return
        }

        let size = NSSize(width:  image.size.width  * zoom,
                          height: image.size.height * zoom)

        imageView = NSImageView(frame: NSRect(origin: .zero, size: size))
        imageView.image        = einkSimulate(image)
        imageView.imageScaling = .scaleProportionallyUpOrDown

        window = KeyWindow(
            contentRect: NSRect(origin: .zero, size: size),
            styleMask:   .borderless,
            backing:     .buffered,
            defer:       false
        )
        window.contentView     = imageView
        window.backgroundColor = .white
        window.isOpaque        = true
        window.hasShadow       = true
        window.center()
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)

        // Drain the signal pipe on the main queue and reload on SIGUSR1.
        let src = DispatchSource.makeReadSource(fileDescriptor: sigPipe[0], queue: .main)
        src.setEventHandler { [weak self] in
            var b: UInt8 = 0
            read(sigPipe[0], &b, 1)
            self?.reloadImage()
        }
        src.resume()
        sigSource = src
    }

    func reloadImage() {
        guard let image = NSImage(contentsOfFile: imagePath) else { return }
        imageView.image = einkSimulate(image)
    }

    func applicationWillTerminate(_: Notification) {
        try? FileManager.default.removeItem(atPath: pidFile)
    }

    func applicationShouldTerminateAfterLastWindowClosed(_: NSApplication) -> Bool { true }
}

let app      = NSApplication.shared
app.setActivationPolicy(.regular)
let delegate = AppDelegate()
app.delegate = delegate
app.run()
