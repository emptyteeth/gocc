// Package core is what gocc does, with nothing of how it is shown.
//
// The native window of the desktop app and the HTTP server of the command
// line version both drive an Engine: it discovers devices, controls one of
// them and keeps the playlist. Neither front end can be reached from here,
// so the same logic serves both, and a test can drive it without either.
//
// Every exported method is safe to call from any goroutine. Whoever wants
// to know when something changed registers on the channels a constructor
// takes, or reads a Snapshot when it likes.
package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/free/gocc/cast"
	"github.com/free/gocc/internal/version"
	"github.com/free/gocc/playlist"
	"github.com/free/gocc/radiobrowser"
)

// Controller is a device gocc talks to: a real cast.Client, or a stand-in
// in a test.
type Controller interface {
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

// Options configures an Engine.
type Options struct {
	// DataDir is where the playlist is kept. Empty means the path given to
	// NewPath, or the working directory when there is none.
	DataDir string
	// Iface limits discovery to one network interface. Nil means every
	// interface, which a machine with more than one should not do: see
	// cast.Discovery.Iface.
	Iface *net.Interface
	// OnChange is called after every change to what Snapshot returns. The
	// native window asks for a new frame here; the HTTP server wakes its
	// waiting clients.
	OnChange func()
	// OnStatus is called with every status a device reports. It is the
	// finer grained of the two: a status arrives several times a second
	// while something plays.
	OnStatus func(cast.Status, error)
	// NewController connects to a device. Nil means the real cast.Client;
	// a test replaces it with one that talks to no device at all.
	NewController func(device cast.Device, onChange func(cast.Status, error)) (Controller, error)
	// Discover finds devices and reports them, and those that go away, as
	// they change, until the returned function is called to stop it. Nil
	// means cast.Discovery; a test replaces it with a fixed list.
	Discover func(ctx context.Context, iface *net.Interface, onDevices func([]cast.Device)) (stop func(), err error)
	// Stations reads the radio-browser catalogue. Nil means the real
	// client; a test replaces it with a fixed catalogue.
	Stations StationSource
	// ConnectTimeout bounds connecting to a device. Zero means 15 seconds.
	ConnectTimeout time.Duration
}

// StationSource is the radio-browser catalogue, as the engine uses it.
type StationSource interface {
	Countries(ctx context.Context) ([]radiobrowser.Country, error)
	Stations(ctx context.Context, q radiobrowser.Query) ([]radiobrowser.Station, error)
}

// Engine is the state of gocc and the things it does.
type Engine struct {
	opts Options

	mu         sync.Mutex
	devices    []cast.Device
	selected   string
	stopDisc   func()
	iface      *net.Interface
	interfaces []net.Interface
	client     Controller
	connecting bool
	status     cast.Status
	castErr    error

	// loading is the entry the device was last asked to play, and when.
	// A device takes a moment to buffer a stream and report that it is
	// playing, and until it does, this is what tells the user their
	// request was heard.
	loading      *loading
	loadingTimer *time.Timer

	playlist *playlist.Playlist
	entries  []playlist.Entry
	chosen   int
	volume   float64
	muted    bool
	volumeAt time.Time
	mutedAt  time.Time
	started  bool
	closed   bool

	// favorites is the list of saved stations and hand written entries.
	// It is kept apart from the playlist, which the web version plays.
	favorites *playlist.Playlist
	favs      []playlist.Entry

	// The radio-browser catalogue: what was read, what is asked for, and
	// whether a request is in flight.
	countries   []radiobrowser.Country
	countryCode string
	order       radiobrowser.Order
	stations    []radiobrowser.Station
	query       string
	searching   bool
	searchErr   error
	// searchSeq makes an old answer lose to a newer question: a reply
	// whose number is not the latest is dropped.
	searchSeq uint64
	// countriesSeq does the same for the country list.
	countriesSeq uint64
}

// loadingTimeout is how long a stream asked for may take to start before
// the app stops saying it is working on it. A device that is off, or a
// stream that never buffers, would otherwise leave it waiting forever.
const loadingTimeout = 30 * time.Second

// loading is an entry the device was asked to play, and is not playing
// yet. A device buffers a stream before it reports that it plays it, which
// takes a moment, and the user is otherwise left wondering whether their
// request was heard at all.
type loading struct {
	entry  playlist.Entry
	since  time.Time
	reason string
}

// describe says what is being waited for, as "Buffering on the device" or
// "Requesting Bedroom…".
func (l *loading) describe() string {
	return l.reason
}

// Snapshot is what an Engine is doing, read at once so that a caller sees
// a whole state rather than one changing under it.
type Snapshot struct {
	Devices    []cast.Device
	Interfaces []net.Interface
	Iface      string
	Selected   string
	Connecting bool
	Status     cast.Status
	Err        string
	Playlist   []playlist.Entry
	Chosen     int
	Volume     float64
	Muted      bool

	// Loading says an entry was asked for and the device is not playing it
	// yet, and LoadingEntry is that entry. LoadingReason is what to tell
	// the user, as "Requesting Living Room…" while the command is on its
	// way or "Buffering…" once the device has taken it.
	Loading       bool
	LoadingEntry  string
	LoadingReason string

	// The radio-browser catalogue, and the favorites kept apart from the
	// playlist.
	Countries   []radiobrowser.Country
	CountryCode string
	Order       radiobrowser.Order
	Stations    []radiobrowser.Station
	Query       string
	Searching   bool
	SearchErr   string
	Favorites   []playlist.Entry
}

// New makes an Engine, reading the playlist and listing the network
// interfaces, without touching the network yet.
func New(opts Options) (*Engine, error) {
	if opts.ConnectTimeout == 0 {
		opts.ConnectTimeout = 15 * time.Second
	}
	if opts.NewController == nil {
		opts.NewController = func(device cast.Device, onChange func(cast.Status, error)) (Controller, error) {
			client := cast.NewClient(device, onChange)
			ctx, cancel := context.WithTimeout(context.Background(), opts.ConnectTimeout)
			defer cancel()
			if err := client.Start(ctx); err != nil {
				client.Close()
				return nil, err
			}
			return client, nil
		}
	}
	if opts.Discover == nil {
		opts.Discover = discoverWithCast
	}
	if opts.Stations == nil {
		opts.Stations = radiobrowser.New(radiobrowser.Options{
			UserAgent: "gocc/" + version.Version,
		})
	}

	e := &Engine{opts: opts, chosen: -1, order: radiobrowser.OrderVotes}

	dir := opts.DataDir
	if dir == "" {
		dir = "."
	}
	path := filepath.Join(dir, "playlist.txt")
	loaded, err := playlist.Load(path)
	if err != nil {
		// A playlist that cannot be read is no reason to refuse to
		// start: say so and carry on with an empty list.
		log.Printf("gocc: reading the playlist: %v", err)
		loaded = &playlist.Playlist{Path: path}
	}
	e.playlist = loaded
	e.entries = e.playlist.Entries()

	favPath := filepath.Join(dir, "favorites.txt")
	favs, err := playlist.Load(favPath)
	if err != nil {
		log.Printf("gocc: reading the favorites: %v", err)
		favs = &playlist.Playlist{Path: favPath}
	}
	e.favorites = favs
	e.favs = favs.Entries()

	if ifaces, err := cast.Interfaces(); err == nil {
		e.interfaces = ifaces
	} else {
		log.Printf("gocc: listing the network interfaces: %v", err)
	}
	// An interface given on the command line is the one to listen on; a
	// machine with only one needs no choice made for it.
	if opts.Iface != nil {
		e.iface = opts.Iface
	} else if len(e.interfaces) == 1 {
		e.iface = &e.interfaces[0]
	}
	return e, nil
}

// Start begins discovering devices. It returns at once; the devices appear
// as they answer. A second call does nothing.
func (e *Engine) Start(ctx context.Context) {
	e.mu.Lock()
	if e.started || e.closed {
		e.mu.Unlock()
		return
	}
	e.started = true
	iface, discover := e.iface, e.opts.Discover
	e.mu.Unlock()

	stop, err := discover(ctx, iface, e.devicesChanged)
	if err != nil {
		e.setErr(err)
		return
	}
	e.mu.Lock()
	e.stopDisc = stop
	e.mu.Unlock()
}

// discoverWithCast finds devices with mDNS, and returns a function that
// ends the search.
func discoverWithCast(ctx context.Context, iface *net.Interface, onDevices func([]cast.Device)) (func(), error) {
	d := &cast.Discovery{Iface: iface, OnDevices: onDevices}
	if err := d.Start(ctx); err != nil {
		return nil, err
	}
	return d.Stop, nil
}

// devicesChanged records the devices discovery has found, from its own
// goroutine.
func (e *Engine) devicesChanged(devices []cast.Device) {
	e.mu.Lock()
	e.devices = devices
	e.mu.Unlock()
	e.changed()
}

// SetOnChange replaces the callback that is told when what Snapshot
// returns changed. A front end that only exists after the Engine does, as
// a window, sets it here.
func (e *Engine) SetOnChange(fn func()) {
	e.mu.Lock()
	e.opts.OnChange = fn
	e.mu.Unlock()
}

// changed tells the front end that what Snapshot returns is different. It
// may be called from any goroutine.
func (e *Engine) changed() {
	e.mu.Lock()
	onChange := e.opts.OnChange
	e.mu.Unlock()
	if onChange != nil {
		onChange()
	}
}

// statusChanged takes a status the device reported, from the client's
// goroutine.
func (e *Engine) statusChanged(status cast.Status, err error) {
	e.mu.Lock()
	e.status = status
	if err != nil {
		e.castErr = err
	}
	// Follow the device's volume unless the user has just set it: a
	// slider must not jump back under a hand still moving it.
	if time.Since(e.volumeAt) > 2*time.Second {
		e.volume = status.Volume
	}
	if time.Since(e.mutedAt) > 2*time.Second {
		e.muted = status.Muted
	}
	// What the device reports decides whether the wait is over. Buffering
	// is the wait itself, so it stays; only playing ends it. A device that
	// reports a URL of its own, after a redirect, is playing all the same,
	// and the status card shows what.
	if e.loading != nil {
		switch status.State {
		case cast.StatePlaying:
			e.stopLoadingLocked()
		case cast.StateBuffering:
			e.loading.reason = "Buffering on " + e.deviceNameLocked() + "…"
		default:
			e.loading.reason = "Starting " + e.loading.entry.Name + "…"
		}
	}
	onStatus := e.opts.OnStatus
	e.mu.Unlock()

	if onStatus != nil {
		onStatus(status, err)
	}
	e.changed()
}

// setErr records an error for the front end to show.
func (e *Engine) setErr(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	e.castErr = err
	e.mu.Unlock()
	e.changed()
}

// ReportError records an error for the front end to show, for the mistakes
// a front end finds itself, as a form that does not hold an entry.
func (e *Engine) ReportError(err error) { e.setErr(err) }

// Snapshot reads what the Engine is doing.
func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	// A device that has not reported yet, or one that reported nothing,
	// is idle: the state a front end shows must always be a real one.
	status := e.status
	if status.State == "" {
		status.State = cast.StateIdle
	}

	s := Snapshot{
		Devices:    append([]cast.Device(nil), e.devices...),
		Interfaces: append([]net.Interface(nil), e.interfaces...),
		Selected:   e.selected,
		Connecting: e.connecting,
		Status:     status,
		Playlist:   append([]playlist.Entry(nil), e.entries...),
		Chosen:     e.chosen,
		Volume:     e.volume,
		Muted:      e.muted,
	}
	if e.iface != nil {
		s.Iface = e.iface.Name
	}
	if e.castErr != nil {
		s.Err = e.castErr.Error()
	}
	if e.loading != nil {
		s.Loading = true
		s.LoadingEntry = e.loading.entry.Name
		s.LoadingReason = e.loading.reason
	}
	s.Countries = append([]radiobrowser.Country(nil), e.countries...)
	s.CountryCode = e.countryCode
	s.Order = e.order
	s.Stations = append([]radiobrowser.Station(nil), e.stations...)
	s.Query = e.query
	s.Searching = e.searching
	s.Favorites = append([]playlist.Entry(nil), e.favs...)
	if e.searchErr != nil {
		s.SearchErr = e.searchErr.Error()
	}
	return s
}

// Choose connects to a device, letting go of the one before it. The
// connection is made on a goroutine of its own, so that the caller is not
// held up while a slow device answers.
func (e *Engine) Choose(device cast.Device) {
	e.mu.Lock()
	if e.selected == device.UUID && e.client != nil {
		e.mu.Unlock()
		return
	}
	old := e.client
	e.client = nil
	e.selected = device.UUID
	e.connecting = true
	e.castErr = nil
	e.status = cast.Status{}
	e.mu.Unlock()
	e.clearLoading()
	e.changed()

	go func() {
		if old != nil {
			old.Close()
		}

		client, err := e.opts.NewController(device, e.statusChanged)
		if err != nil {
			e.mu.Lock()
			// The device may have been let go, or another chosen,
			// while this one was connecting.
			if e.selected == device.UUID {
				e.connecting = false
				e.castErr = err
			}
			e.mu.Unlock()
			e.changed()
			return
		}

		e.mu.Lock()
		if e.selected != device.UUID {
			// Another device was chosen while this one connected.
			e.mu.Unlock()
			client.Close()
			return
		}
		e.client = client
		e.connecting = false
		// The device's own volume is the truth about the slider.
		status := client.Status()
		e.volume, e.muted = status.Volume, status.Muted
		e.mu.Unlock()

		e.changed()
	}()
}

// ChooseByUUID connects to the device with this UUID among those found.
func (e *Engine) ChooseByUUID(uuid string) error {
	e.mu.Lock()
	device, ok := e.deviceLocked(uuid)
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("no device with uuid %q", uuid)
	}
	e.Choose(device)
	return nil
}

// deviceLocked finds a device among those found. The caller holds mu.
func (e *Engine) deviceLocked(uuid string) (cast.Device, bool) {
	for _, d := range e.devices {
		if d.UUID == uuid {
			return d, true
		}
	}
	return cast.Device{}, false
}

// Disconnect lets go of the device being controlled.
func (e *Engine) Disconnect() {
	e.mu.Lock()
	client := e.client
	e.client = nil
	e.selected = ""
	e.status = cast.Status{}
	e.connecting = false
	e.mu.Unlock()
	e.clearLoading()
	e.changed()
	if client != nil {
		go client.Close()
	}
}

// client returns the device being controlled, if any.
func (e *Engine) controller() Controller {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.client
}

// Play sends an entry's URL to the device being controlled.
//
// The device is not playing it yet when this returns: it takes a moment to
// buffer a stream, and the state says so until it does. See the Loading
// fields of Snapshot.
func (e *Engine) Play(entry playlist.Entry) {
	client := e.controller()
	if client == nil {
		e.setErr(cast.ErrNoClient)
		return
	}
	e.beginLoading(entry)
	client.Play(entry.URL, entry.ContentType)
}

// beginLoading records that an entry was asked for, so that the front end
// can say so until the device plays it.
func (e *Engine) beginLoading(entry playlist.Entry) {
	e.mu.Lock()
	if e.loadingTimer != nil {
		e.loadingTimer.Stop()
	}
	e.loading = &loading{
		entry:  entry,
		since:  time.Now(),
		reason: "Requesting " + e.deviceNameLocked() + "…",
	}
	// A device that never answers must not leave the app saying it is
	// working on something forever.
	e.loadingTimer = time.AfterFunc(loadingTimeout, e.loadingTimedOut)
	e.mu.Unlock()
	e.changed()
}

// loadingTimedOut says a stream asked for never started.
func (e *Engine) loadingTimedOut() {
	e.mu.Lock()
	if e.loading == nil {
		e.mu.Unlock()
		return
	}
	name := e.loading.entry.Name
	e.loading, e.loadingTimer = nil, nil
	e.castErr = fmt.Errorf("%s did not start playing within %s", name, loadingTimeout)
	e.mu.Unlock()
	e.changed()
}

// clearLoading forgets a request the device has answered.
func (e *Engine) clearLoading() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopLoadingLocked()
}

// stopLoadingLocked ends the wait and its timer. The caller holds mu.
func (e *Engine) stopLoadingLocked() {
	if e.loadingTimer != nil {
		e.loadingTimer.Stop()
		e.loadingTimer = nil
	}
	e.loading = nil
}

// deviceNameLocked names the device being controlled. The caller holds mu.
func (e *Engine) deviceNameLocked() string {
	for _, d := range e.devices {
		if d.UUID == e.selected {
			switch {
			case d.Name != "":
				return d.Name
			case d.Addr != "":
				return d.Addr
			}
		}
	}
	return "the device"
}

// PlayChosen plays the entry chosen in the playlist.
func (e *Engine) PlayChosen() {
	e.mu.Lock()
	i, entries := e.chosen, e.entries
	e.mu.Unlock()
	if i >= 0 && i < len(entries) {
		e.Play(entries[i])
	}
}

// PlayIndex plays the entry at i.
func (e *Engine) PlayIndex(i int) error {
	e.mu.Lock()
	entries := e.entries
	e.mu.Unlock()
	if i < 0 || i >= len(entries) {
		return fmt.Errorf("no entry at %d", i)
	}
	e.Play(entries[i])
	return nil
}

// Pause pauses the device.
func (e *Engine) Pause() {
	if client := e.controller(); client != nil {
		client.Pause()
	}
}

// Resume plays again.
func (e *Engine) Resume() {
	if client := e.controller(); client != nil {
		client.Resume()
	}
}

// Stop stops the device's media.
func (e *Engine) Stop() {
	e.clearLoading()
	if client := e.controller(); client != nil {
		client.Stop()
	}
}

// TogglePlay pauses what plays, and plays what is paused.
func (e *Engine) TogglePlay() {
	status := e.Status()
	switch status.State {
	case cast.StatePlaying, cast.StateBuffering:
		e.Pause()
	case cast.StatePaused:
		e.Resume()
	default:
		// Nothing is loaded: play the chosen entry instead.
		e.PlayChosen()
	}
}

// Status returns the last status the device reported.
func (e *Engine) Status() cast.Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

// SetVolume sets the device's volume, remembering that the user asked for
// it. A level outside 0 to 1 is brought into it, rather than refused: a
// slider cannot ask for one, but a caller of the API can.
func (e *Engine) SetVolume(level float64) {
	level = clampVolume(level)
	e.mu.Lock()
	e.volume = level
	e.volumeAt = time.Now()
	e.mu.Unlock()
	if client := e.controller(); client != nil {
		client.SetVolume(level)
	}
	e.changed()
}

// clampVolume brings a level into the range a device accepts.
func clampVolume(level float64) float64 {
	switch {
	case level < 0:
		return 0
	case level > 1:
		return 1
	}
	return level
}

// SetMuted turns the device's sound off, or back on.
func (e *Engine) SetMuted(muted bool) {
	e.mu.Lock()
	e.muted = muted
	e.mutedAt = time.Now()
	e.mu.Unlock()
	if client := e.controller(); client != nil {
		client.SetMuted(muted)
	}
	e.changed()
}

// Refresh reads the device's status again.
func (e *Engine) Refresh() {
	if client := e.controller(); client != nil {
		client.Refresh()
	}
}

// UseInterface limits discovery to one network interface, and looks again.
// A nil interface listens on every one.
func (e *Engine) UseInterface(iface *net.Interface) {
	e.stop()
	e.mu.Lock()
	e.iface, e.devices = iface, nil
	e.mu.Unlock()
	e.Start(context.Background())
}

// Rediscover stops discovery and starts it again, to find devices that
// appeared without being announced.
func (e *Engine) Rediscover() {
	e.stop()
	e.mu.Lock()
	e.devices = nil
	e.mu.Unlock()
	e.Start(context.Background())
}

// stop ends discovery, if one is running, and lets it start again.
func (e *Engine) stop() {
	e.mu.Lock()
	stop := e.stopDisc
	e.stopDisc, e.started = nil, false
	e.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// Close lets go of the device and stops discovery.
func (e *Engine) Close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	client, stop := e.client, e.stopDisc
	e.client, e.stopDisc = nil, nil
	if e.loadingTimer != nil {
		e.loadingTimer.Stop()
		e.loadingTimer = nil
	}
	e.mu.Unlock()

	if client != nil {
		client.Close()
	}
	if stop != nil {
		stop()
	}
}

// AddEntry adds an entry to the playlist and saves it.
func (e *Engine) AddEntry(entry playlist.Entry) error {
	if err := e.playlist.Add(entry); err != nil {
		err = fmt.Errorf("playlist: %w", err)
		e.setErr(err)
		return err
	}
	e.reloadEntries()
	return nil
}

// RemoveEntry takes the entry at i out of the playlist and saves it.
func (e *Engine) RemoveEntry(i int) error {
	if err := e.playlist.Remove(i); err != nil {
		err = fmt.Errorf("playlist: %w", err)
		e.setErr(err)
		return err
	}
	e.mu.Lock()
	if e.chosen >= e.playlist.Len() {
		e.chosen = e.playlist.Len() - 1
	}
	e.mu.Unlock()
	e.reloadEntries()
	return nil
}

// MoveEntry moves the entry at from to the index to, and keeps it chosen.
func (e *Engine) MoveEntry(from, to int) error {
	if err := e.playlist.Move(from, to); err != nil {
		err = fmt.Errorf("playlist: %w", err)
		e.setErr(err)
		return err
	}
	e.mu.Lock()
	e.chosen = to
	if to > from {
		e.chosen = to - 1
	}
	e.mu.Unlock()
	e.reloadEntries()
	return nil
}

// ChooseEntry records which playlist entry is chosen.
func (e *Engine) ChooseEntry(i int) {
	e.mu.Lock()
	e.chosen = i
	e.mu.Unlock()
	e.changed()
}

// reloadEntries reads the playlist back into the digest the front ends
// read.
func (e *Engine) reloadEntries() {
	entries := e.playlist.Entries()
	e.mu.Lock()
	e.entries = entries
	e.mu.Unlock()
	e.changed()
}

// PlaylistPath is the file the playlist is kept in.
func (e *Engine) PlaylistPath() string { return e.playlist.Path }

// ValidateEntry reports what is wrong with an entry before it is saved.
// The playlist package checks the same things, but the message is worth
// having where the form is, and both front ends want the same one.
func ValidateEntry(entry playlist.Entry) error {
	if entry.Name == "" {
		return fmt.Errorf("the entry needs a name")
	}
	if entry.URL == "" {
		return fmt.Errorf("the entry needs a URL")
	}
	if !strings.Contains(entry.URL, "://") {
		return fmt.Errorf("%q is not an absolute URL, as http://host/stream.mp3", entry.URL)
	}
	if !strings.HasPrefix(entry.URL, "http://") && !strings.HasPrefix(entry.URL, "https://") {
		return fmt.Errorf("%q is not an http or https URL", entry.URL)
	}
	if strings.ContainsAny(entry.URL, " \t\n") {
		return fmt.Errorf("the URL holds a space")
	}
	return nil
}

// FavoritesPath is the file the favorites are kept in.
func (e *Engine) FavoritesPath() string { return e.favorites.Path }

// LoadCountries reads the countries the catalogue has stations in. It
// returns at once; the list arrives through the snapshot.
func (e *Engine) LoadCountries() {
	e.mu.Lock()
	e.countriesSeq++
	seq := e.countriesSeq
	e.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		countries, err := e.opts.Stations.Countries(ctx)

		e.mu.Lock()
		// A later request has answered already: this one is stale.
		if seq != e.countriesSeq {
			e.mu.Unlock()
			return
		}
		picked := false
		if err == nil {
			e.countries = countries
			// The first country is the one with the most stations, which
			// is a fair place to start when the user did not say. Choosing
			// it also reads its stations: the browse pane would otherwise
			// wait for a choice that was already made for it.
			if e.countryCode == "" && len(countries) > 0 {
				e.countryCode = countries[0].Code
				picked = true
			}
		}
		e.mu.Unlock()

		if err != nil {
			e.setErr(fmt.Errorf("radio-browser: reading the countries: %w", err))
			return
		}
		e.changed()
		if picked {
			e.LoadStations()
		}
	}()
}

// SetCountry limits the stations to one country, by its ISO code, and
// reads them again. An empty code lists every country.
func (e *Engine) SetCountry(code string) {
	e.mu.Lock()
	if e.countryCode == code {
		e.mu.Unlock()
		return
	}
	e.countryCode = code
	e.mu.Unlock()
	e.LoadStations()
}

// SetOrder changes how the stations are sorted, and reads them again.
func (e *Engine) SetOrder(order radiobrowser.Order) {
	if !order.Valid() {
		return
	}
	e.mu.Lock()
	if e.order == order {
		e.mu.Unlock()
		return
	}
	e.order = order
	e.mu.Unlock()
	e.LoadStations()
}

// SetStationQuery keeps the stations whose name contains q, and reads them
// again. An empty q lists every station of the country.
func (e *Engine) SetStationQuery(q string) {
	q = strings.TrimSpace(q)
	e.mu.Lock()
	if e.query == q {
		e.mu.Unlock()
		return
	}
	e.query = q
	e.mu.Unlock()
	e.LoadStations()
}

// LoadStations reads the stations for the country, order and query the
// engine holds. It returns at once; the list arrives through the snapshot.
func (e *Engine) LoadStations() {
	e.mu.Lock()
	e.searchSeq++
	seq := e.searchSeq
	// The previous answer is no longer what is asked for. Keep it while
	// the new one loads, so that the grid does not empty on every change.
	e.searching = true
	e.searchErr = nil
	e.mu.Unlock()
	e.changed()

	go func() {
		e.mu.Lock()
		q := radiobrowser.Query{
			CountryCode: e.countryCode,
			Order:       e.order,
			Reverse:     e.order != radiobrowser.OrderName,
			Name:        e.query,
			Limit:       radiobrowser.DefaultLimit,
		}
		e.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		stations, err := e.opts.Stations.Stations(ctx, q)

		e.mu.Lock()
		if seq != e.searchSeq {
			// A newer question is being answered; this answer is stale.
			e.mu.Unlock()
			return
		}
		e.searching = false
		if err != nil {
			e.searchErr = err
			e.mu.Unlock()
			e.changed()
			return
		}
		e.stations = stations
		e.searchErr = nil
		e.mu.Unlock()
		e.changed()
	}()
}

// PlayStation plays station i of the catalogue.
func (e *Engine) PlayStation(i int) error {
	e.mu.Lock()
	if i < 0 || i >= len(e.stations) {
		e.mu.Unlock()
		return fmt.Errorf("no station at %d", i)
	}
	entry := e.stations[i].Entry()
	e.mu.Unlock()
	if entry.URL == "" {
		return fmt.Errorf("the station has no stream URL")
	}
	e.Play(entry)
	return nil
}

// AddStationFavorite saves station i of the catalogue to the favorites.
func (e *Engine) AddStationFavorite(i int) error {
	e.mu.Lock()
	if i < 0 || i >= len(e.stations) {
		e.mu.Unlock()
		return fmt.Errorf("no station at %d", i)
	}
	entry := e.stations[i].Entry()
	e.mu.Unlock()
	return e.AddFavorite(entry)
}

// AddFavorite saves an entry to the favorites, unless it is there already.
func (e *Engine) AddFavorite(entry playlist.Entry) error {
	if err := e.favorites.Add(entry); err != nil {
		// Adding what is already saved is not a mistake: the button that
		// asks is a toggle, and pressing it twice is not an error.
		if errors.Is(err, playlist.ErrDuplicate) {
			return nil
		}
		return fmt.Errorf("favorites: %w", err)
	}
	e.reloadFavorites()
	return nil
}

// RemoveFavorite takes entry i out of the favorites.
func (e *Engine) RemoveFavorite(i int) error {
	if err := e.favorites.Remove(i); err != nil {
		return fmt.Errorf("favorites: %w", err)
	}
	e.reloadFavorites()
	return nil
}

// PlayFavorite plays entry i of the favorites.
func (e *Engine) PlayFavorite(i int) error {
	e.mu.Lock()
	if i < 0 || i >= len(e.favs) {
		e.mu.Unlock()
		return fmt.Errorf("no favorite at %d", i)
	}
	entry := e.favs[i]
	e.mu.Unlock()
	e.Play(entry)
	return nil
}

// IsFavorite reports whether the favorites hold an entry for url.
func (e *Engine) IsFavorite(url string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, entry := range e.favs {
		if entry.URL == url {
			return true
		}
	}
	return false
}

// ToggleFavorite saves station i of the catalogue, or takes it out when it
// is saved already.
func (e *Engine) ToggleFavorite(url string) error {
	e.mu.Lock()
	var at = -1
	for i, entry := range e.favs {
		if entry.URL == url {
			at = i
			break
		}
	}
	if at >= 0 {
		e.mu.Unlock()
		return e.RemoveFavorite(at)
	}
	var station *radiobrowser.Station
	for i := range e.stations {
		if e.stations[i].URLToPlay() == url {
			station = &e.stations[i]
			break
		}
	}
	e.mu.Unlock()

	if station == nil {
		return fmt.Errorf("no station with URL %q", url)
	}
	return e.AddFavorite(station.Entry())
}

// reloadFavorites reads the favorites back into the snapshot.
func (e *Engine) reloadFavorites() {
	entries := e.favorites.Entries()
	e.mu.Lock()
	e.favs = entries
	e.mu.Unlock()
	e.changed()
}
