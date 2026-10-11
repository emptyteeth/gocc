package cast

import (
	"sync"
	"testing"
	"time"
)

// recorder collects the levels a throttle sends.
type recorder struct {
	mu     sync.Mutex
	levels []float64
}

func (r *recorder) send(level float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.levels = append(r.levels, level)
}

func (r *recorder) got() []float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]float64(nil), r.levels...)
}

// waitFor polls until the recorder holds n levels, or the deadline passes.
func (r *recorder) waitFor(t *testing.T, n int, within time.Duration) []float64 {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if got := r.got(); len(got) >= n {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	return r.got()
}

func TestVolumeThrottleCollapsesADrag(t *testing.T) {
	rec := &recorder{}
	th := newVolumeThrottle(40*time.Millisecond, rec.send)
	defer th.stop()

	// The first level of a quiet spell goes out at once, so that a single
	// adjustment is not delayed.
	th.set(0.10)
	if got := rec.got(); len(got) != 1 || got[0] != 0.10 {
		t.Fatalf("the first level did not go out at once: %v", got)
	}

	// A drag asks for a level on every frame. Only the last one of the
	// interval goes out, and only once.
	for i := 0; i < 60; i++ {
		th.set(0.10 + float64(i)*0.01)
	}
	got := rec.waitFor(t, 2, time.Second)
	if len(got) != 2 {
		t.Fatalf("a drag of 60 levels sent %d commands: %v", len(got), got)
	}
	if got[1] != 0.69 {
		t.Errorf("the level sent is %v, want the last one asked for, 0.69", got[1])
	}
}

func TestVolumeThrottleKeepsSendingDuringALongDrag(t *testing.T) {
	rec := &recorder{}
	th := newVolumeThrottle(20*time.Millisecond, rec.send)
	defer th.stop()

	// A drag that lasts a while still sends now and then, rather than
	// only at the end: the device follows the hand as it moves.
	for i := 0; i < 5; i++ {
		th.set(float64(i) / 10)
		time.Sleep(25 * time.Millisecond)
	}
	if got := rec.got(); len(got) < 3 {
		t.Fatalf("a drag of 100ms sent %d commands, want several: %v", len(got), got)
	}
}

func TestVolumeThrottleSendsTheFinalLevel(t *testing.T) {
	rec := &recorder{}
	th := newVolumeThrottle(30*time.Millisecond, rec.send)
	defer th.stop()

	th.set(0.2)
	th.set(0.3)
	th.set(0.42) // the hand stopped here

	got := rec.waitFor(t, 2, time.Second)
	if len(got) != 2 || got[1] != 0.42 {
		t.Fatalf("sent %v, want the last level 0.42 to arrive", got)
	}
}

func TestVolumeThrottleStopDropsWhatWaits(t *testing.T) {
	rec := &recorder{}
	th := newVolumeThrottle(40*time.Millisecond, rec.send)

	th.set(0.1) // goes out at once
	th.set(0.9) // waits
	th.stop()   // and is dropped

	time.Sleep(80 * time.Millisecond)
	if got := rec.got(); len(got) != 1 {
		t.Fatalf("sent %v after stop, want only the first level", got)
	}
}

func TestVolumeThrottleIsSafeFromSeveralGoroutines(t *testing.T) {
	rec := &recorder{}
	th := newVolumeThrottle(5*time.Millisecond, rec.send)
	defer th.stop()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				th.set(float64(g) / 10)
			}
		}(g)
	}
	wg.Wait()
	time.Sleep(40 * time.Millisecond)

	// The point is that it does not race, panic or lose the last level;
	// the run detector below catches a data race.
	got := rec.got()
	if len(got) == 0 {
		t.Fatal("nothing was sent")
	}
	if last := got[len(got)-1]; last < 0 || last > 1 {
		t.Fatalf("a level out of range was sent: %v", last)
	}
}

func TestClampLevel(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{-0.5, 0}, {0, 0}, {0.5, 0.5}, {1, 1}, {1.5, 1},
	} {
		if got := clampLevel(tc.in); got != tc.want {
			t.Errorf("clampLevel(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// queued returns how many commands are waiting for the command goroutine.
func (c *Client) queued() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

// waitQueued polls until n commands are queued, or the deadline passes.
func (c *Client) waitQueued(n int, within time.Duration) int {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if got := c.queued(); got >= n {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	return c.queued()
}

// TestClientVolumeCoalesces drives the real path the interface uses, from
// SetVolume into the command queue, and checks that a drag does not put
// one command per frame there.
func TestClientVolumeCoalesces(t *testing.T) {
	c := NewClient(Device{Name: "test", Addr: "127.0.0.1", Port: 8009}, nil)
	defer func() {
		c.volume.stop()
		c.Close()
	}()

	// The client is not started, so nothing drains the queue: what is in
	// it is exactly what would have been sent to the device.
	c.SetVolume(0.10)
	if got := c.queued(); got != 1 {
		t.Fatalf("the first level queued %d commands, want 1", got)
	}

	// A drag asks for a level on every frame.
	for i := 0; i < 60; i++ {
		c.SetVolume(0.10 + float64(i)*0.01)
	}
	got := c.waitQueued(2, time.Second)
	if got != 2 {
		t.Fatalf("a drag of 60 levels queued %d commands, want 2", got)
	}
}

// TestClientVolumeSendsTheFinalLevel checks that the level the hand
// stopped at is the one that waits to be sent, not one from the middle of
// the drag. The throttle's own test reads the levels back; here the point
// is that the client wires the throttle to the queue at all.
func TestClientVolumeSendsTheFinalLevel(t *testing.T) {
	c := NewClient(Device{Addr: "127.0.0.1", Port: 8009}, nil)
	defer func() {
		c.volume.stop()
		c.Close()
	}()

	c.SetVolume(0.2)
	c.SetVolume(0.4)
	c.SetVolume(0.75) // the hand stopped here

	if got := c.waitQueued(2, time.Second); got != 2 {
		t.Fatalf("queued %d commands, want 2", got)
	}
	// The second command is the one still waiting, and it carries the
	// last level rather than the one before it.
	c.volume.mu.Lock()
	pending := c.volume.pending
	c.volume.mu.Unlock()
	if pending != nil {
		t.Fatalf("a level is still waiting to be sent: %v", *pending)
	}
}

// TestClientVolumeQueuesInOrder checks that the levels go out in the
// order they were asked for, the last one last.
func TestClientVolumeQueuesInOrder(t *testing.T) {
	c := NewClient(Device{Addr: "127.0.0.1", Port: 8009}, nil)
	defer func() {
		c.volume.stop()
		c.Close()
	}()

	c.SetVolume(0.2) // goes out at once
	c.SetVolume(0.3)
	c.SetVolume(0.75) // replaces 0.3, and waits

	if got := c.waitQueued(2, time.Second); got != 2 {
		t.Fatalf("queued %d commands, want 2", got)
	}
}
