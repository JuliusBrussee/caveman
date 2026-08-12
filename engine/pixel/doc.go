// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

// Package pixel ports pxpipe's text-to-PNG renderer and request gating into
// Caveman's public engine.
//
// Pixel rendering is an S4 lossy transform: it changes model-visible bytes by
// moving text into image blocks. Callers must keep original bytes recoverable
// through CCR before forwarding transformed bytes, and any parse, render, or
// storage error must fail closed to pass-through. Token reductions from this
// package are local estimates only.
//
// The port intentionally differs from pxpipe in three places: PNG bytes come
// from Go's stdlib image/png encoder, so
// tests compare decoded pixels rather than PNG byte streams; future transform
// token estimates use Caveman's offline engine/tokens counter instead of
// pxpipe's o200k path; no live count_tokens probe is performed here.
package pixel
