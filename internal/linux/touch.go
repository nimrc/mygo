//go:build linux && (amd64 || arm64)

package linux

import (
	"math"
	"slices"

	"github.com/egoist/mygo/internal/platform"
)

// Fingers on a touchscreen do what the mouse and a touchpad do, as on
// phones: a tap clicks where it lands, and one held there longPress or
// longer right-clicks, as Windows's press and hold does; a finger moving
// further than touchSlop scrolls the content under it along with it,
// and two pinch to zoom between them and scroll as they move together.
// GTK made the first finger the mouse, which pressed where it landed and
// selected text as it moved: the surface takes the touches instead
// (surface.go).
//
// With pointer set (WindowOptions.TouchPointer, or MYGO_TOUCH_POINTER=1),
// one finger is the mouse instead, as on a desktop touchscreen: it
// presses where it lands, moves the pointer as it moves and lets go
// where it lifts, so it drags a slider or a selection. A second finger
// cancels the press, and two pinch and scroll as they do otherwise.
// There is no long press then: the finger pressed as it landed.
const (
	touchSlop = 8   // DIPs
	longPress = 500 // milliseconds
)

// touchGesture follows the fingers on a surface, by their GDK sequences,
// and turns them into surface events.
type touchGesture struct {
	// pointer makes one finger the mouse; pressing tells that the only
	// finger down presses.
	pointer, pressing bool
	// fingers are down, in the order they landed; the first two count.
	fingers []touchFinger
	// tap tells that the only finger down stayed within touchSlop of
	// where it landed, at startX, startY at time start.
	tap            bool
	startX, startY float64
	start          uint32
	// x, y is where the fingers last scrolled from: the finger, or the
	// midpoint of two, dist apart.
	x, y, dist float64
}

type touchFinger struct {
	seq  uintptr
	x, y float64
}

// begin puts a finger down at x, y at time t, in milliseconds.
func (g *touchGesture) begin(seq uintptr, x, y float64, t uint32) []platform.SurfaceEvent {
	if g.find(seq) >= 0 {
		return nil
	}
	g.fingers = append(g.fingers, touchFinger{seq, x, y})
	g.anchor()
	if g.pointer {
		switch {
		case len(g.fingers) == 1:
			g.pressing = true
			return []platform.SurfaceEvent{{Kind: platform.PointerDown, X: x, Y: y}}
		case g.pressing:
			g.pressing = false
			return []platform.SurfaceEvent{{Kind: platform.PointerCancel}}
		}
		return nil
	}
	g.tap = len(g.fingers) == 1
	if g.tap {
		g.startX, g.startY, g.start = x, y, t
	}
	return nil
}

// reset forgets the fingers, whose ends went unseen, canceling the press
// of one.
func (g *touchGesture) reset() []platform.SurfaceEvent {
	g.fingers = g.fingers[:0]
	if !g.pressing {
		return nil
	}
	g.pressing = false
	return []platform.SurfaceEvent{{Kind: platform.PointerCancel}}
}

// move moves a finger to x, y.
func (g *touchGesture) move(seq uintptr, x, y float64) []platform.SurfaceEvent {
	i := g.find(seq)
	if i < 0 {
		return nil
	}
	if g.pressing {
		if g.fingers[i].x == x && g.fingers[i].y == y {
			return nil
		}
		g.fingers[i].x, g.fingers[i].y = x, y
		return []platform.SurfaceEvent{{Kind: platform.PointerMove, X: x, Y: y}}
	}
	g.fingers[i].x, g.fingers[i].y = x, y
	var out []platform.SurfaceEvent
	switch {
	case i >= 2:
	case len(g.fingers) == 1:
		if g.tap && math.Hypot(x-g.startX, y-g.startY) < touchSlop {
			break
		}
		g.tap = false
		out = g.scroll(out, x, y)
	default:
		a, b := g.fingers[0], g.fingers[1]
		mx, my := (a.x+b.x)/2, (a.y+b.y)/2
		out = g.scroll(out, mx, my)
		if d := math.Hypot(a.x-b.x, a.y-b.y); d > 0 {
			if g.dist > 0 && d != g.dist {
				out = append(out, platform.SurfaceEvent{Kind: platform.PointerZoom, X: mx, Y: my, Scale: d / g.dist})
			}
			g.dist = d
		}
	}
	return out
}

// end lifts a finger at x, y at time t; canceled, as when the compositor
// takes the touch for its own gesture, it neither moves nor taps.
func (g *touchGesture) end(seq uintptr, x, y float64, t uint32, canceled bool) []platform.SurfaceEvent {
	i := g.find(seq)
	if i < 0 {
		return nil
	}
	var out []platform.SurfaceEvent
	switch {
	case g.pressing && canceled:
		g.pressing = false
		out = append(out, platform.SurfaceEvent{Kind: platform.PointerCancel})
	case g.pressing:
		g.pressing = false
		out = append(out, platform.SurfaceEvent{Kind: platform.PointerUp, X: x, Y: y})
	case !canceled:
		out = g.move(seq, x, y)
	}
	g.fingers = slices.Delete(g.fingers, i, i+1)
	if len(g.fingers) > 0 {
		g.anchor()
		return out
	}
	if g.tap && !canceled {
		button := 0
		if t-g.start >= longPress {
			button = 1
		}
		out = append(out,
			platform.SurfaceEvent{Kind: platform.PointerDown, X: g.startX, Y: g.startY, Button: button},
			platform.SurfaceEvent{Kind: platform.PointerUp, X: g.startX, Y: g.startY, Button: button})
	}
	g.tap = false
	// Nothing hovers once the fingers are gone.
	return append(out, platform.SurfaceEvent{Kind: platform.PointerLeave})
}

// scroll scrolls the content at the last point along to x, y, following
// the fingers: the other way from a scroll bar's thumb.
func (g *touchGesture) scroll(out []platform.SurfaceEvent, x, y float64) []platform.SurfaceEvent {
	if x != g.x || y != g.y {
		out = append(out, platform.SurfaceEvent{Kind: platform.PointerScroll, X: g.x, Y: g.y, DX: g.x - x, DY: g.y - y, Precise: true})
	}
	g.x, g.y = x, y
	return out
}

// anchor scrolls and pinches from where the fingers are now.
func (g *touchGesture) anchor() {
	a := g.fingers[0]
	g.x, g.y, g.dist = a.x, a.y, 0
	if len(g.fingers) > 1 {
		b := g.fingers[1]
		g.x, g.y, g.dist = (a.x+b.x)/2, (a.y+b.y)/2, math.Hypot(a.x-b.x, a.y-b.y)
	}
}

func (g *touchGesture) find(seq uintptr) int {
	return slices.IndexFunc(g.fingers, func(f touchFinger) bool { return f.seq == seq })
}
