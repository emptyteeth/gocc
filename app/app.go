// Package app is gocc's application: the devices it finds, the one it
// controls, and the playlist it plays from.
//
// The view runs on the main thread for every frame, while discovery,
// connections and commands run on goroutines of their own. The state is
// split in two along that line: what the view reads and writes lives on
// the main thread and needs no lock, and what the goroutines write is
// guarded by mu and handed to the view as a snapshot. Goroutines ask for
// a new frame when they are done, so the view never waits for the network.
package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/free/gocc/cast"
	"github.com/free/gocc/playlist"
)

// controller is what the app asks of a device: the real cast.Client, and
// a stand-in where there is no device, as in a test.
type controller interface {
	Status() cast.Status
	Play(url, contentType string)
	Pause()
	Resume()
	Stop()
	SetVolume(level float64)
	SetMuted(muted bool)
	Refresh()
	Close()
}

// App is everything the window shows and the state behind it.
type App struct {
	// mu guards the state the goroutines write: the devices, the
	// connection and the status. The view reads it as a snapshot.
	mu sync.Mutex

	win        *mygo.Window
	devices    []cast.Device
	selected   string
	discovery  *cast.Discovery
	iface      *net.Interface
	interfaces []net.Interface
	client     controller
	connecting bool
	status     cast.Status
	castErr    error

	// The state only the main thread touches: the view binds its
	// controls to it, and the updates from the device arrive through
	// Window.Update, which runs on the main thread too.
	playlist   *playlist.Playlist
	entries    []playlist.Entry
	chosen     int
	list       ui.ListState
	deviceList ui.ListState
	deviceSel  int
	volume     float64
	muted      bool
	volumeAt   time.Time
	mutedAt    time.Time
	newName    string
	newURL     string
	newType    string
}

// snapshot is the state the goroutines own, read at once so that the view
// sees a whole state rather than one changing under it.
type snapshot struct {
	devices    []cast.Device
	selected   string
	connecting bool
	status     cast.Status
	err        error
	iface      *net.Interface
	interfaces []net.Interface
}

// New makes the app, reading the playlist and listing the network
// interfaces, without touching the network yet.
//
// The playlist lives in the app's own data directory, so that it is found
// again whatever the app is started from.
func New() (*App, error) {
	a := &App{chosen: -1}

	path := "playlist.txt"
	if dir, err := mygo.App.Path(mygo.PathUserData); err == nil {
		path = filepath.Join(dir, "playlist.txt")
	} else {
		log.Printf("gocc: finding the data directory: %v", err)
	}

	loaded, err := playlist.Load(path)
	if err != nil {
		// A playlist that cannot be read is no reason to refuse to
		// start: say so and carry on with an empty list.
		log.Printf("gocc: reading the playlist: %v", err)
		loaded = &playlist.Playlist{Path: path}
	}
	a.playlist = loaded
	a.entries = a.playlist.Entries()

	if ifaces, err := cast.Interfaces(); err == nil {
		a.interfaces = ifaces
		// A machine with one interface needs no choice made for it.
		if len(ifaces) == 1 {
			a.iface = &ifaces[0]
		}
	}
	return a, nil
}

// Attach gives the app its window, which it asks for new frames.
func (a *App) Attach(win *mygo.Window) {
	a.mu.Lock()
	a.win = win
	a.mu.Unlock()
}

// View builds the window, and is its content.
func (a *App) View(c *ui.Context) { a.view(c) }

// Start begins discovering devices. It returns at once; the devices
// appear as they answer.
func (a *App) Start() {
	a.mu.Lock()
	if a.discovery != nil {
		a.mu.Unlock()
		return
	}
	d := &cast.Discovery{Iface: a.iface, OnDevices: a.devicesChanged}
	a.discovery = d
	a.mu.Unlock()

	if err := d.Start(context.Background()); err != nil {
		a.setErr(err)
	}
}

// devicesChanged records the devices discovery has found, from its own
// goroutine.
func (a *App) devicesChanged(devices []cast.Device) {
	a.mu.Lock()
	a.devices = devices
	a.mu.Unlock()
	a.invalidate()
}

// invalidate asks for a new frame, from any goroutine.
func (a *App) invalidate() {
	a.mu.Lock()
	win := a.win
	a.mu.Unlock()
	if win != nil {
		win.Invalidate()
	}
}

// onMain runs fn on the main thread, and asks for a frame after it.
func (a *App) onMain(fn func()) {
	a.mu.Lock()
	win := a.win
	a.mu.Unlock()
	if win == nil {
		return
	}
	win.Update(fn)
}

// setErr records an error for the window to show.
func (a *App) setErr(err error) {
	if err == nil {
		return
	}
	a.mu.Lock()
	a.castErr = err
	a.mu.Unlock()
	a.invalidate()
}

// snapshot reads the state the goroutines own.
func (a *App) snapshot() snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return snapshot{
		devices:    append([]cast.Device(nil), a.devices...),
		selected:   a.selected,
		connecting: a.connecting,
		status:     a.status,
		err:        a.castErr,
		iface:      a.iface,
		interfaces: append([]net.Interface(nil), a.interfaces...),
	}
}

// chooseDevice makes the client the app controls a device through. A test
// replaces it with one that talks to no device at all.
var chooseDevice = func(device cast.Device, onChange func(cast.Status, error)) (controller, error) {
	client := cast.NewClient(device, onChange)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// Choose connects to a device, letting go of the one before it. The
// connection is made on a goroutine of its own, so that the window keeps
// drawing while a slow device answers.
func (a *App) Choose(device cast.Device) {
	a.mu.Lock()
	if a.selected == device.UUID && a.client != nil {
		a.mu.Unlock()
		return
	}
	old := a.client
	a.client = nil
	a.selected = device.UUID
	a.connecting = true
	a.castErr = nil
	a.status = cast.Status{}
	a.mu.Unlock()
	a.invalidate()

	go func() {
		if old != nil {
			old.Close()
		}

		client, err := chooseDevice(device, a.statusChanged)
		if err != nil {
			a.mu.Lock()
			// The device may have been let go, or another chosen,
			// while this one was connecting.
			if a.selected == device.UUID {
				a.connecting = false
				a.castErr = err
			}
			a.mu.Unlock()
			a.invalidate()
			return
		}

		a.mu.Lock()
		if a.selected != device.UUID {
			// Another device was chosen while this one connected.
			a.mu.Unlock()
			client.Close()
			return
		}
		a.client = client
		a.connecting = false
		a.mu.Unlock()

		// The device's own volume is the truth about the slider.
		a.onMain(func() {
			status := client.Status()
			a.volume = status.Volume
			a.muted = status.Muted
		})
		a.invalidate()
	}()
}

// Disconnect lets go of the device being controlled.
func (a *App) Disconnect() {
	a.mu.Lock()
	client := a.client
	a.client = nil
	a.selected = ""
	a.status = cast.Status{}
	a.connecting = false
	a.mu.Unlock()
	a.invalidate()
	if client != nil {
		go client.Close()
	}
}

// statusChanged takes a status the device reported, from the client's
// goroutine.
func (a *App) statusChanged(status cast.Status, err error) {
	a.mu.Lock()
	a.status = status
	if err != nil {
		a.castErr = err
	}
	a.mu.Unlock()

	a.onMain(func() {
		// Follow the device's volume unless the user has just set it:
		// the slider must not jump back under a hand still moving it.
		if time.Since(a.volumeAt) > 2*time.Second {
			a.volume = status.Volume
		}
		if time.Since(a.mutedAt) > 2*time.Second {
			a.muted = status.Muted
		}
	})
}

// currentClient returns the client of the device being controlled.
func (a *App) currentClient() controller {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.client
}

// Play sends an entry's URL to the device being controlled.
func (a *App) Play(e playlist.Entry) {
	client := a.currentClient()
	if client == nil {
		a.setErr(cast.ErrNoClient)
		return
	}
	client.Play(e.URL, e.ContentType)
}

// PlayChosen plays the entry chosen in the playlist.
func (a *App) PlayChosen() {
	if i := a.chosen; i >= 0 && i < len(a.entries) {
		a.Play(a.entries[i])
	}
}

// Pause pauses the device.
func (a *App) Pause() {
	if client := a.currentClient(); client != nil {
		client.Pause()
	}
}

// Resume plays again.
func (a *App) Resume() {
	if client := a.currentClient(); client != nil {
		client.Resume()
	}
}

// Stop stops the device's media.
func (a *App) Stop() {
	if client := a.currentClient(); client != nil {
		client.Stop()
	}
}

// TogglePlay pauses what plays, and plays what is paused.
func (a *App) TogglePlay() {
	status := a.currentStatus()
	switch status.State {
	case cast.StatePlaying, cast.StateBuffering:
		a.Pause()
	case cast.StatePaused:
		a.Resume()
	default:
		// Nothing is loaded: play the chosen entry instead.
		a.PlayChosen()
	}
}

// currentStatus returns the last status the device reported.
func (a *App) currentStatus() cast.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// SetVolume sets the device's volume, remembering that the user asked for
// it. It runs on the main thread, from the view.
func (a *App) SetVolume(level float64) {
	a.volume = level
	a.volumeAt = time.Now()
	if client := a.currentClient(); client != nil {
		client.SetVolume(level)
	}
}

// SetMuted turns the device's sound off, or back on.
func (a *App) SetMuted(muted bool) {
	a.muted = muted
	a.mutedAt = time.Now()
	if client := a.currentClient(); client != nil {
		client.SetMuted(muted)
	}
}

// Refresh reads the device's status again.
func (a *App) Refresh() {
	if client := a.currentClient(); client != nil {
		client.Refresh()
	}
}

// UseInterface limits discovery to one network interface, and looks again.
func (a *App) UseInterface(iface *net.Interface) {
	a.mu.Lock()
	d := a.discovery
	a.discovery, a.iface, a.devices = nil, iface, nil
	a.mu.Unlock()

	if d != nil {
		d.Stop()
	}
	a.Start()
}

// Rediscover stops discovery and starts it again, to find devices that
// appeared without being announced.
func (a *App) Rediscover() {
	a.mu.Lock()
	d := a.discovery
	a.discovery, a.devices = nil, nil
	a.mu.Unlock()
	if d != nil {
		d.Stop()
	}
	a.Start()
}

// Close lets go of the device and stops discovery.
func (a *App) Close() {
	a.mu.Lock()
	client, discovery := a.client, a.discovery
	a.client, a.discovery = nil, nil
	a.mu.Unlock()

	if client != nil {
		client.Close()
	}
	if discovery != nil {
		discovery.Stop()
	}
}

// AddEntry adds an entry to the playlist and saves it.
func (a *App) AddEntry(e playlist.Entry) {
	if err := a.playlist.Add(e); err != nil {
		a.setErr(fmt.Errorf("playlist: %w", err))
		return
	}
	a.reloadEntries()
}

// RemoveEntry takes an entry out of the playlist and saves it.
func (a *App) RemoveEntry(i int) {
	if err := a.playlist.Remove(i); err != nil {
		a.setErr(fmt.Errorf("playlist: %w", err))
		return
	}
	if a.chosen >= a.playlist.Len() {
		a.chosen = a.playlist.Len() - 1
	}
	a.reloadEntries()
}

// MoveEntry moves an entry up or down the playlist, and keeps it chosen.
func (a *App) MoveEntry(from, to int) {
	if err := a.playlist.Move(from, to); err != nil {
		a.setErr(fmt.Errorf("playlist: %w", err))
		return
	}
	a.chosen = to
	if to > from {
		a.chosen = to - 1
	}
	a.reloadEntries()
}

// reloadEntries reads the playlist back into the view's copy.
func (a *App) reloadEntries() {
	a.entries = a.playlist.Entries()
	a.invalidate()
}
