package main

import (
	"bytes"
	"errors"
	"image"
	"math"
	"sort"
	"sync"
	"time"
)

// MARK: How far the picture moved

// tracker keeps the luma of a window's box every 30 ms from start to stop, and adds up how far its rows moved from one
// picture to the next in region (inside box).
type tracker struct {
	box, region image.Rectangle
	mu          sync.Mutex
	frames      [][]uint8
	at          []time.Time // when each picture came
	stopc       chan struct{}
	done        chan struct{}
}

func startTracker(c *probeConn, box, region image.Rectangle) *tracker {
	t := &tracker{box: box, region: region, stopc: make(chan struct{}), done: make(chan struct{})}
	grab := func() {
		var l []uint8
		c.WithFramebuffer(func(fb *image.RGBA) { l = lumaPlane(fb, box) })
		t.mu.Lock()
		if n := len(t.frames); n == 0 || !bytes.Equal(t.frames[n-1], l) {
			t.frames = append(t.frames, l)
			t.at = append(t.at, time.Now())
		}
		t.mu.Unlock()
	}
	grab()
	go func() {
		defer close(t.done)
		tick := time.NewTicker(30 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-t.stopc:
				grab()
				return
			case <-tick.C:
				grab()
			}
		}
	}()
	return t
}

// stop ends tracking: how far the rows went up in all (pixels; < 0 down), and when they last moved. A picture that
// matches the last one at no shift (part of the window redrawn in between, say) is left out; the last one must match.
func (t *tracker) stop() (float64, time.Time, error) {
	close(t.stopc)
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	w := t.box.Dx()
	r := t.region.Sub(t.box.Min)
	total, base, matched := 0, 0, true
	var moved time.Time
	for i := 1; i < len(t.frames); i++ {
		s, cost := rowShift(t.frames[base], t.frames[i], w, r, r.Dy()*2/3)
		if matched = cost <= wheelMatch; matched {
			if s != 0 {
				moved = t.at[i]
			}
			total += s
			base = i
		}
	}
	if !matched {
		return 0, moved, errors.New("the window's last picture matches the one before at no shift: something else changed in it")
	}
	return float64(total), moved, nil
}

// wheelMatch is the most two pictures of the same rows, shifted, differ (mean absolute luma).
const wheelMatch = 12

// lumaPlane is box's luma, a byte a pixel, row by row (outside fb: 0).
func lumaPlane(fb *image.RGBA, box image.Rectangle) []uint8 {
	out := make([]uint8, box.Dx()*box.Dy())
	for y := box.Min.Y; y < box.Max.Y; y++ {
		if y < fb.Rect.Min.Y || y >= fb.Rect.Max.Y {
			continue
		}
		row := out[(y-box.Min.Y)*box.Dx():]
		for x := box.Min.X; x < box.Max.X; x++ {
			if x < fb.Rect.Min.X || x >= fb.Rect.Max.X {
				continue
			}
			o := fb.PixOffset(x, y)
			row[x-box.Min.X] = uint8((299*int(fb.Pix[o]) + 587*int(fb.Pix[o+1]) + 114*int(fb.Pix[o+2])) / 1000)
		}
	}
	return out
}

// rowShift is how far the rows of region (in a plane w wide) went up from a to b: b's row y is a's row y+s, s within
// ±most, chosen where they differ least; and that difference (mean absolute luma). Only shifts that keep at least a
// third of region's rows inside the plane count.
func rowShift(a, b []uint8, w int, region image.Rectangle, most int) (int, float64) {
	h := len(a) / w
	cost := func(s, ystep, xstep int) (float64, bool) {
		sum, n, rows := 0, 0, 0
		for y := region.Min.Y; y < region.Max.Y; y += ystep {
			ya := y + s
			if ya < 0 || ya >= h {
				continue
			}
			rows += ystep
			ra, rb := a[ya*w:], b[y*w:]
			for x := region.Min.X; x < region.Max.X; x += xstep {
				d := int(ra[x]) - int(rb[x])
				if d < 0 {
					d = -d
				}
				sum += d
				n++
			}
		}
		if rows*3 < region.Dy() || n == 0 {
			return 0, false
		}
		return float64(sum) / float64(n), true
	}
	// Coarse (every 2nd row, 4th column), then each shift near the best few at full detail.
	type cand struct {
		s int
		c float64
	}
	var cands []cand
	for s := -most; s <= most; s++ {
		if c, ok := cost(s, 2, 4); ok {
			cands = append(cands, cand{s, c})
		}
	}
	if len(cands) == 0 {
		return 0, math.Inf(1)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].c < cands[j].c })
	best, bestC := 0, math.Inf(1)
	seen := map[int]bool{}
	for _, k := range cands[:min(4, len(cands))] {
		for s := k.s - 2; s <= k.s+2; s++ {
			if s < -most || s > most || seen[s] {
				continue
			}
			seen[s] = true
			if c, ok := cost(s, 1, 1); ok && (c < bestC || c == bestC && abs(s) < abs(best)) {
				best, bestC = s, c
			}
		}
	}
	return best, bestC
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
