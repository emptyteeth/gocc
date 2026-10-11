package cast

import (
	"sync"
	"time"
)

// volumeInterval is the shortest time between two volume commands sent to
// a device.
//
// A device plays its own volume-change sound for every command it
// receives, and a drag of the slider asks for a new level on every frame,
// which is far more than a device keeps up with. Sending at most one
// command per interval keeps both the sound and the traffic to something
// a device handles, while the slider still follows the hand.
var volumeInterval = 200 * time.Millisecond

// volumeThrottle sends the level a device has not been told yet, at most
// once every interval.
//
// A level asked for while another waits replaces it, so that the level the
// hand stopped at is the one that goes out last. The first level of a
// quiet spell goes out at once, so that a single adjustment is not
// delayed by the interval.
type volumeThrottle struct {
	interval time.Duration
	send     func(level float64)

	mu      sync.Mutex
	pending *float64
	sentAt  time.Time
	timer   *time.Timer
}

// newVolumeThrottle makes a throttle that hands levels to send.
func newVolumeThrottle(interval time.Duration, send func(level float64)) *volumeThrottle {
	return &volumeThrottle{interval: interval, send: send}
}

// set asks for a level to be sent, now or once the interval has passed.
func (t *volumeThrottle) set(level float64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.pending = &level
	if t.timer != nil {
		// A send is already waiting, and it takes the latest level.
		return
	}
	wait := t.interval - time.Since(t.sentAt)
	if wait <= 0 {
		t.sendLocked()
		return
	}
	t.timer = time.AfterFunc(wait, t.flush)
}

// flush sends the level that was waiting, if any.
func (t *volumeThrottle) flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timer = nil
	t.sendLocked()
}

// sendLocked sends the level that was waiting, if any, and notes when.
func (t *volumeThrottle) sendLocked() {
	if t.pending == nil {
		return
	}
	level := *t.pending
	t.pending = nil
	t.sentAt = time.Now()
	t.send(level)
}

// stop ends a send that is waiting, without making it.
func (t *volumeThrottle) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	t.pending = nil
}

// clampLevel keeps a volume in the range a device accepts.
func clampLevel(level float64) float64 {
	switch {
	case level < 0:
		return 0
	case level > 1:
		return 1
	}
	return level
}
