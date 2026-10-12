package cast

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vishen/go-chromecast/application"
)

// PlayerState is what a device is doing with its media.
type PlayerState string

// The states a device reports.
const (
	StateIdle      PlayerState = "IDLE"
	StatePlaying   PlayerState = "PLAYING"
	StatePaused    PlayerState = "PAUSED"
	StateBuffering PlayerState = "BUFFERING"
)

// Status is what a device is doing: what it plays, whether it is paused,
// and how loud it is.
type Status struct {
	// State is the player's state.
	State PlayerState `json:"state"`
	// Title, Artist and URL describe what plays.
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	URL    string `json:"url,omitempty"`
	// Volume is the level from 0 to 1, Muted whether sound is off.
	Volume float64 `json:"volume"`
	Muted  bool    `json:"muted"`
	// IdleReason says why an idle player is idle: "FINISHED", "CANCELLED",
	// "INTERRUPTED" or "ERROR".
	IdleReason string `json:"idleReason,omitempty"`
	// CurrentTime is how far into the media the device is, in seconds.
	CurrentTime float64 `json:"currentTime,omitempty"`
}

// Playing reports whether the device is playing or about to.
func (s Status) Playing() bool {
	return s.State == StatePlaying || s.State == StateBuffering
}

// Describe names the state for the app to show.
func (s Status) Describe() string {
	switch s.State {
	case StatePlaying:
		return "Playing"
	case StatePaused:
		return "Paused"
	case StateBuffering:
		return "Buffering"
	case StateIdle:
		if s.IdleReason != "" {
			return "Idle (" + s.IdleReason + ")"
		}
		return "Idle"
	}
	return "Unknown"
}

// Client controls one device.
//
// Every command runs on one goroutine of its own, in the order it was
// asked for, because the underlying library is not safe to use from
// several goroutines at once. Asking for a command never blocks the
// caller, so the interface thread can call any method directly.
type Client struct {
	device Device

	mu       sync.Mutex
	app      *application.Application
	queue    []func(*application.Application)
	status   Status
	lastErr  error
	closed   bool
	onChange func(Status, error)

	// volume sends the levels the slider asks for, at most one every
	// interval, so that a drag does not flood the device.
	volume *volumeThrottle

	wake chan struct{}
	quit chan struct{}
	done chan struct{}
}

// NewClient makes a client for a device, without connecting yet. Call
// Start to connect. onChange is called with every status the device
// reports, from the client's own goroutine.
func NewClient(device Device, onChange func(Status, error)) *Client {
	c := &Client{
		device:   device,
		onChange: onChange,
		wake:     make(chan struct{}, 1),
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	c.volume = newVolumeThrottle(volumeInterval, c.sendVolume)
	return c
}

// Start connects to the device and serves commands until Close. It
// returns once the connection is made, or the error that stopped it.
// Status updates arrive through the client's callback.
func (c *Client) Start(ctx context.Context) error {
	app := application.NewApplication(
		// The library's own cache is a directory of the user's home,
		// which an app has no business writing to.
		application.WithCacheDisabled(true),
		// Two tries: a device that is off answers nothing, and the user
		// would rather see the error than wait for five attempts.
		application.WithConnectionRetries(2),
	)
	if c.device.iface != nil {
		app.SetIface(c.device.iface)
	}

	// Start blocks while it connects, and the library gives it no context
	// to cancel, so run it where a slow device cannot hold up the caller.
	ready := make(chan error, 1)
	go func() { ready <- app.Start(c.device.Addr, c.device.Port) }()

	select {
	case err := <-ready:
		if err != nil {
			app.Close(false)
			return fmt.Errorf("cast: connecting to %s: %w", c.device, err)
		}
	case <-ctx.Done():
		go func() {
			<-ready
			app.Close(false)
		}()
		return ctx.Err()
	}

	c.mu.Lock()
	c.app = app
	c.mu.Unlock()

	go c.serve(app)
	c.publish()
	return nil
}

// serve runs every command on this one goroutine, in the order the app
// asked for them.
func (c *Client) serve(app *application.Application) {
	defer close(c.done)
	for {
		select {
		case <-c.quit:
			return
		case <-c.wake:
			for {
				c.mu.Lock()
				if len(c.queue) == 0 {
					c.mu.Unlock()
					break
				}
				cmd := c.queue[0]
				c.queue = c.queue[1:]
				c.mu.Unlock()

				cmd(app)
				c.publish()
			}
		}
	}
}

// Close ends the client and disconnects from the device.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	app := c.app
	c.mu.Unlock()

	// A level waiting to be sent has no device to go to any more.
	c.volume.stop()
	close(c.quit)
	if app == nil {
		return
	}
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
	}
	// Closing the connection is not concurrent safe either, so it happens
	// once the command goroutine has stopped.
	app.Close(false)
}

// Status returns the last status the device reported.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// do queues fn to run on the command goroutine, and returns at once: the
// caller is never held up by a device that is slow to answer. The
// device's status is read back afterwards.
func (c *Client) do(fn func(*application.Application) error) {
	c.enqueue(fn, true)
}

// doQuiet queues fn like do, without reading the status back. Use it for
// a command whose outcome the device reports on its own, so that a stream
// of them does not double the traffic.
func (c *Client) doQuiet(fn func(*application.Application) error) {
	c.enqueue(fn, false)
}

// enqueue puts a command on the queue the command goroutine drains.
func (c *Client) enqueue(fn func(*application.Application) error, refresh bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.queue = append(c.queue, func(app *application.Application) {
		err := fn(app)
		c.mu.Lock()
		c.lastErr = err
		c.mu.Unlock()
		if !refresh {
			return
		}
		// Read the status back, so that the app shows what the device
		// did rather than what it was asked to do.
		if err := app.Update(); err != nil {
			c.mu.Lock()
			c.lastErr = fmt.Errorf("cast: reading the status of %s: %w", c.device, err)
			c.mu.Unlock()
		}
	})
	c.mu.Unlock()

	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// publish builds a Status from the library's state and hands it to the
// app's callback.
func (c *Client) publish() {
	c.mu.Lock()
	app := c.app
	c.mu.Unlock()
	if app == nil {
		return
	}

	var status Status
	if _, media, _ := app.Status(); media != nil {
		status.State = PlayerState(media.PlayerState)
		status.Title = media.Media.Metadata.Title
		status.Artist = media.Media.Metadata.Artist
		status.URL = media.Media.ContentId
		status.IdleReason = media.IdleReason
		status.CurrentTime = float64(media.CurrentTime)
		status.Volume = float64(media.Volume.Level)
		status.Muted = media.Volume.Muted
	}
	if volume := app.Volume(); volume != nil {
		status.Volume = float64(volume.Level)
		status.Muted = volume.Muted
	}
	if status.State == "" {
		status.State = StateIdle
	}

	c.mu.Lock()
	c.status = status
	err := c.lastErr
	onChange := c.onChange
	c.mu.Unlock()

	if onChange != nil {
		onChange(status, err)
	}
}

// Play sends a URL to the device to play.
//
// contentType may be empty, in which case it is worked out from the URL:
// its extension first, so that a stream answers at once, and a HEAD
// request only when the extension says nothing.
func (c *Client) Play(url, contentType string) {
	c.do(func(app *application.Application) error {
		ct := contentType
		if ct == "" {
			ct = ContentType(context.Background(), url)
		}
		// detach is true so that the call returns as soon as the device
		// has been told, rather than waiting for the stream to end.
		return app.Load(url, 0, ct, false, true, false)
	})
}

// Pause pauses what plays.
func (c *Client) Pause() {
	c.do(func(app *application.Application) error { return app.Pause() })
}

// Resume plays what is paused.
func (c *Client) Resume() {
	c.do(func(app *application.Application) error { return app.Unpause() })
}

// Stop stops the media.
func (c *Client) Stop() {
	c.do(func(app *application.Application) error { return app.StopMedia() })
}

// SetVolume asks the device for a level, from 0 to 1.
//
// A drag of the slider asks for a level on every frame, and a device
// answers each command with a sound of its own, so the levels are sent at
// most one every interval: the last one asked for is the one that goes
// out, and a level asked for after a quiet spell goes out at once.
func (c *Client) SetVolume(level float64) {
	c.volume.set(clampLevel(level))
}

// sendVolume puts a level on the command queue. It is what the throttle
// calls, on the throttle's timer goroutine or on the caller's.
//
// The status is not read back: a device reports a new volume by itself,
// so asking for it again would double the traffic a drag makes, and the
// round trip would make the command goroutine fall behind the hand.
func (c *Client) sendVolume(level float64) {
	c.doQuiet(func(app *application.Application) error { return app.SetVolume(float32(level)) })
}

// SetMuted turns the sound off, or back on.
func (c *Client) SetMuted(muted bool) {
	c.do(func(app *application.Application) error { return app.SetMuted(muted) })
}

// Refresh reads the device's status again.
func (c *Client) Refresh() {
	c.do(func(*application.Application) error { return nil })
}

// ErrNoClient is returned when a command needs a connected device and
// there is none.
var ErrNoClient = errors.New("cast: no device is connected")
