package core

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/free/gocc/cast"
	"github.com/free/gocc/playlist"
	"github.com/free/gocc/radiobrowser"
)

// fakeDevice stands in for a device, so that the engine can be driven in a
// test without one on the network.
type fakeDevice struct {
	status   cast.Status
	onChange func(cast.Status, error)
	played   []string
	types    []string
	volume   []float64
	muted    []bool
	paused   int
	resumed  int
	stopped  int
	closed   int
	// quiet makes Play record the request without reporting that the
	// device plays it, as a real device does while it buffers. The test
	// then decides when the device catches up.
	quiet bool
}

// report tells the engine what the device is doing now, as the real
// client does.
func (f *fakeDevice) report() {
	if f.onChange != nil {
		f.onChange(f.status, nil)
	}
}

func (f *fakeDevice) Status() cast.Status { return f.status }

func (f *fakeDevice) Play(url, contentType string) {
	f.played = append(f.played, url)
	f.types = append(f.types, contentType)
	if f.quiet {
		return
	}
	f.status.State = cast.StatePlaying
	f.status.URL = url
	f.report()
}

func (f *fakeDevice) Pause()  { f.paused++; f.status.State = cast.StatePaused; f.report() }
func (f *fakeDevice) Resume() { f.resumed++; f.status.State = cast.StatePlaying; f.report() }
func (f *fakeDevice) Stop() {
	f.stopped++
	f.status = cast.Status{State: cast.StateIdle}
	f.report()
}
func (f *fakeDevice) Refresh() {}
func (f *fakeDevice) Close()   { f.closed++ }

func (f *fakeDevice) SetVolume(level float64) {
	f.volume = append(f.volume, level)
	f.status.Volume = level
	f.report()
}

func (f *fakeDevice) SetMuted(muted bool) {
	f.muted = append(f.muted, muted)
	f.status.Muted = muted
	f.report()
}

// testDevice is the device the tests connect to.
var testDevice = cast.Device{
	UUID: "uuid-1",
	Name: "Living Room",
	Addr: "10.0.0.5",
	Port: 8009,
}

// newTestEngine makes an engine with a playlist in a temporary directory
// and a device already connected, without touching the network.
func newTestEngine(t *testing.T, entries ...playlist.Entry) (*Engine, *fakeDevice) {
	t.Helper()
	dir := t.TempDir()
	device := &fakeDevice{}

	e, err := New(Options{DataDir: dir,
		NewController: func(cast.Device, func(cast.Status, error)) (Controller, error) {
			return device, nil
		},
		Discover: func(_ context.Context, _ *net.Interface, onDevices func([]cast.Device)) (func(), error) {
			onDevices([]cast.Device{testDevice})
			return func() {}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)

	for _, entry := range entries {
		if err := e.AddEntry(entry); err != nil {
			t.Fatal(err)
		}
	}
	device.onChange = e.statusChanged
	e.Start(context.Background())
	e.Choose(testDevice)
	waitFor(t, func() bool { return e.Snapshot().Selected == testDevice.UUID && !e.Snapshot().Connecting })
	return e, device
}

// waitFor polls until ok, or fails the test after a moment.
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for the engine")
}

func TestPlayAndTransport(t *testing.T) {
	e, device := newTestEngine(t, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})

	if err := e.PlayIndex(0); err != nil {
		t.Fatal(err)
	}
	if len(device.played) != 1 || device.played[0] != "http://radio1.com/1.mp3" {
		t.Fatalf("played %v", device.played)
	}
	if got := e.Status().State; got != cast.StatePlaying {
		t.Fatalf("state = %q, want playing", got)
	}

	e.TogglePlay() // playing, so it pauses
	if device.paused != 1 {
		t.Fatalf("paused %d times", device.paused)
	}
	e.TogglePlay() // paused, so it resumes
	if device.resumed != 1 {
		t.Fatalf("resumed %d times", device.resumed)
	}
	e.Stop()
	if device.stopped != 1 {
		t.Fatalf("stopped %d times", device.stopped)
	}
}

func TestTogglePlayWithNothingLoadedPlaysTheChosenEntry(t *testing.T) {
	e, device := newTestEngine(t, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})
	e.ChooseEntry(0)
	e.TogglePlay()
	if len(device.played) != 1 {
		t.Fatalf("played %v, want the chosen entry", device.played)
	}
}

func TestVolumeAndMute(t *testing.T) {
	e, device := newTestEngine(t)

	e.SetVolume(0.4)
	if len(device.volume) != 1 || device.volume[0] != 0.4 {
		t.Fatalf("volume sent %v", device.volume)
	}
	e.SetMuted(true)
	if len(device.muted) != 1 || !device.muted[0] {
		t.Fatalf("mute sent %v", device.muted)
	}
	// The engine keeps what the user asked for, and the device confirms it.
	if s := e.Snapshot(); s.Volume != 0.4 || !s.Muted {
		t.Fatalf("snapshot holds %v muted=%v", s.Volume, s.Muted)
	}
}

func TestWithoutADevicePlayReportsAnError(t *testing.T) {
	e, _ := newTestEngine(t, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})
	e.Disconnect()
	e.ChooseEntry(0)
	e.PlayChosen()
	waitFor(t, func() bool { return e.Snapshot().Err != "" })
}

func TestPlaylistIsSavedAndReported(t *testing.T) {
	e, _ := newTestEngine(t)
	if err := e.AddEntry(playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"}); err != nil {
		t.Fatal(err)
	}
	if s := e.Snapshot(); len(s.Playlist) != 1 || s.Playlist[0].Name != "radio1" {
		t.Fatalf("snapshot holds %+v", s.Playlist)
	}

	// It is on disk, so that the next run finds it.
	again, err := playlist.Load(e.PlaylistPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Entries(); len(got) != 1 || got[0].URL != "http://radio1.com/1.mp3" {
		t.Fatalf("saved %+v", got)
	}
}

func TestRemoveAndMoveKeepTheChoice(t *testing.T) {
	e, _ := newTestEngine(t,
		playlist.Entry{Name: "a", URL: "http://x.com/a"},
		playlist.Entry{Name: "b", URL: "http://x.com/b"},
		playlist.Entry{Name: "c", URL: "http://x.com/c"},
	)
	e.ChooseEntry(1)                          // b
	if err := e.MoveEntry(1, 3); err != nil { // to the end
		t.Fatal(err)
	}
	if names := entryNames(e); names != "a,c,b" {
		t.Fatalf("after move: %v", names)
	}
	if got := e.Snapshot().Chosen; got != 2 {
		t.Errorf("the moved entry is not still chosen: %d", got)
	}
	if err := e.RemoveEntry(2); err != nil {
		t.Fatal(err)
	}
	if names := entryNames(e); names != "a,c" {
		t.Fatalf("after remove: %v", names)
	}
	// The choice cannot point past the end of what is left.
	if got := e.Snapshot().Chosen; got > 1 {
		t.Errorf("the choice is past the end: %d", got)
	}
}

func TestChooseByUUIDRejectsAnUnknownDevice(t *testing.T) {
	e, _ := newTestEngine(t)
	if err := e.ChooseByUUID("nope"); err == nil {
		t.Error("choosing an unknown device should fail")
	}
}

func TestAddEntryRejectsWhatThePlaylistWould(t *testing.T) {
	e, _ := newTestEngine(t)
	if err := e.AddEntry(playlist.Entry{Name: "bad", URL: "not a url"}); err == nil {
		t.Error("a bad entry should be refused")
	}
	if s := e.Snapshot(); len(s.Playlist) != 0 {
		t.Fatalf("a bad entry was added: %+v", s.Playlist)
	}
}

func TestValidateEntry(t *testing.T) {
	for _, tc := range []struct {
		entry playlist.Entry
		ok    bool
	}{
		{playlist.Entry{Name: "a", URL: "http://x.com/a.mp3"}, true},
		{playlist.Entry{Name: "a", URL: "https://x.com/a"}, true},
		{playlist.Entry{Name: "", URL: "http://x.com/a"}, false},
		{playlist.Entry{Name: "a", URL: ""}, false},
		{playlist.Entry{Name: "a", URL: "ftp://x.com/a"}, false},
		{playlist.Entry{Name: "a", URL: "not a url"}, false},
		{playlist.Entry{Name: "a", URL: "http://x.com/a b"}, false},
	} {
		err := ValidateEntry(tc.entry)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateEntry(%+v) = %v, wanted ok=%v", tc.entry, err, tc.ok)
		}
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	e, device := newTestEngine(t)
	e.Close()
	e.Close() // must not panic or close the device twice
	if device.closed > 1 {
		t.Errorf("the device was closed %d times", device.closed)
	}
}

func entryNames(e *Engine) string {
	names := ""
	for i, entry := range e.Snapshot().Playlist {
		if i > 0 {
			names += ","
		}
		names += entry.Name
	}
	return names
}

func TestLoadingIsReportedUntilTheDevicePlays(t *testing.T) {
	e, device := newTestEngine(t, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})
	device.quiet = true

	// Asking for an entry says so at once, before the device has answered.
	e.PlayIndex(0)
	waitFor(t, func() bool { return e.Snapshot().Loading })
	s := e.Snapshot()
	if s.LoadingEntry != "radio1" {
		t.Errorf("loading entry = %q, want radio1", s.LoadingEntry)
	}
	if s.LoadingReason == "" {
		t.Error("no reason is given for the wait")
	}
	if !strings.Contains(s.LoadingReason, "Living Room") {
		t.Errorf("the reason does not name the device: %q", s.LoadingReason)
	}

	// The device reporting that it plays the entry ends the wait.
	device.status = cast.Status{State: cast.StatePlaying, URL: "http://radio1.com/1.mp3"}
	device.report()
	waitFor(t, func() bool { return !e.Snapshot().Loading })
}

func TestLoadingEndsWhenTheDevicePlaysSomethingElse(t *testing.T) {
	// A device that reports a URL of its own, after a redirect, is taken
	// to be playing what was asked for rather than left waiting.
	e, device := newTestEngine(t, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})
	device.quiet = true
	e.PlayIndex(0)
	waitFor(t, func() bool { return e.Snapshot().Loading })

	device.status = cast.Status{State: cast.StatePlaying, URL: "http://cdn.example/redirected"}
	device.report()
	waitFor(t, func() bool { return !e.Snapshot().Loading })
}

func TestLoadingSurvivesADeviceStillBuffering(t *testing.T) {
	e, device := newTestEngine(t, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})
	device.quiet = true
	e.PlayIndex(0)
	waitFor(t, func() bool { return e.Snapshot().Loading })

	// A device that says it is idle is not playing it yet.
	device.status = cast.Status{State: cast.StateIdle}
	device.report()
	time.Sleep(50 * time.Millisecond)
	if !e.Snapshot().Loading {
		t.Error("the wait ended while the device reported nothing playing")
	}
	if got := e.Snapshot().LoadingReason; !strings.Contains(got, "radio1") {
		t.Errorf("the reason does not name the entry: %q", got)
	}
}

func TestStopEndsTheWait(t *testing.T) {
	e, device := newTestEngine(t, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})
	device.quiet = true
	e.PlayIndex(0)
	waitFor(t, func() bool { return e.Snapshot().Loading })
	e.Stop()
	waitFor(t, func() bool { return !e.Snapshot().Loading })
}

func TestDisconnectEndsTheWait(t *testing.T) {
	e, device := newTestEngine(t, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})
	device.quiet = true
	e.PlayIndex(0)
	waitFor(t, func() bool { return e.Snapshot().Loading })
	e.Disconnect()
	waitFor(t, func() bool { return !e.Snapshot().Loading })
}

func TestPlayingWithoutADeviceDoesNotWait(t *testing.T) {
	e, _ := newTestEngine(t, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})
	e.Disconnect()
	e.ChooseEntry(0)
	e.PlayChosen()
	waitFor(t, func() bool { return e.Snapshot().Err != "" })
	if e.Snapshot().Loading {
		t.Error("the app waits for a stream it could not have asked for")
	}
}

// fakeSource is a radio-browser catalogue that answers with what it holds,
// and records what it was asked.
type fakeSource struct {
	mu        sync.Mutex
	countries []radiobrowser.Country
	stations  []radiobrowser.Station
	queries   []radiobrowser.Query
	err       error
}

func (f *fakeSource) Countries(context.Context) ([]radiobrowser.Country, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.countries, nil
}

func (f *fakeSource) Stations(_ context.Context, q radiobrowser.Query) ([]radiobrowser.Station, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	return f.stations, nil
}

// asked returns the queries the catalogue received.
func (f *fakeSource) asked() []radiobrowser.Query {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]radiobrowser.Query(nil), f.queries...)
}

// newTestEngineWithSource is newTestEngine with a catalogue the test
// controls.
func newTestEngineWithSource(t *testing.T) (*Engine, *fakeDevice, *fakeSource) {
	t.Helper()
	device := &fakeDevice{}
	source := &fakeSource{
		countries: []radiobrowser.Country{
			{Name: "Germany", Code: "DE", Stations: 6533},
			{Name: "France", Code: "FR", Stations: 3926},
		},
		stations: []radiobrowser.Station{
			{UUID: "s1", Name: "Rock Antenne", URL: "http://a", URLResolved: "https://a/stream.mp3", Codec: "MP3", CountryCode: "DE"},
			{UUID: "s2", Name: "Jazz", URL: "http://b", Codec: "AAC", CountryCode: "DE"},
		},
	}
	e, err := New(Options{
		DataDir: t.TempDir(),
		NewController: func(cast.Device, func(cast.Status, error)) (Controller, error) {
			return device, nil
		},
		Discover: func(_ context.Context, _ *net.Interface, onDevices func([]cast.Device)) (func(), error) {
			onDevices([]cast.Device{testDevice})
			return func() {}, nil
		},
		Stations: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	device.onChange = e.statusChanged
	e.Start(context.Background())
	e.Choose(testDevice)
	waitFor(t, func() bool { return e.Snapshot().Selected == testDevice.UUID && !e.Snapshot().Connecting })
	return e, device, source
}

func TestLoadCountriesAndStations(t *testing.T) {
	e, _, source := newTestEngineWithSource(t)

	e.LoadCountries()
	waitFor(t, func() bool { return len(e.Snapshot().Countries) == 2 })
	if got := e.Snapshot().CountryCode; got != "DE" {
		t.Errorf("the first country was not chosen: %q", got)
	}

	e.LoadStations()
	waitFor(t, func() bool { return len(e.Snapshot().Stations) == 2 && !e.Snapshot().Searching })
	if got := e.Snapshot().Stations[0].Name; got != "Rock Antenne" {
		t.Errorf("first station = %q", got)
	}

	// The query carries the country and the default order.
	queries := source.asked()
	if len(queries) == 0 {
		t.Fatal("the catalogue was never asked")
	}
	q := queries[len(queries)-1]
	if q.CountryCode != "DE" || q.Order != radiobrowser.OrderVotes || !q.Reverse {
		t.Errorf("query = %+v", q)
	}
}

func TestSetCountryAndOrderReload(t *testing.T) {
	e, _, source := newTestEngineWithSource(t)
	e.LoadStations()
	waitFor(t, func() bool { return len(source.asked()) == 1 })

	e.SetCountry("FR")
	waitFor(t, func() bool { return len(source.asked()) == 2 })
	if got := source.asked()[1].CountryCode; got != "FR" {
		t.Errorf("country = %q", got)
	}

	e.SetOrder(radiobrowser.OrderBitrate)
	waitFor(t, func() bool { return len(source.asked()) == 3 })
	if got := source.asked()[2].Order; got != radiobrowser.OrderBitrate {
		t.Errorf("order = %q", got)
	}

	// Setting the same values again must not make another request.
	e.SetCountry("FR")
	e.SetOrder(radiobrowser.OrderBitrate)
	time.Sleep(50 * time.Millisecond)
	if got := len(source.asked()); got != 3 {
		t.Errorf("an unchanged filter made a request: %d queries", got)
	}
}

func TestStationQueryIsTrimmed(t *testing.T) {
	e, _, source := newTestEngineWithSource(t)
	e.SetStationQuery("  jazz  ")
	waitFor(t, func() bool { return len(source.asked()) == 1 })
	if got := source.asked()[0].Name; got != "jazz" {
		t.Errorf("query name = %q, want the trimmed word", got)
	}
	if got := e.Snapshot().Query; got != "jazz" {
		t.Errorf("snapshot query = %q", got)
	}
}

func TestPlayStation(t *testing.T) {
	e, device, _ := newTestEngineWithSource(t)
	e.LoadStations()
	waitFor(t, func() bool { return len(e.Snapshot().Stations) == 2 })

	if err := e.PlayStation(0); err != nil {
		t.Fatal(err)
	}
	if len(device.played) != 1 || device.played[0] != "https://a/stream.mp3" {
		t.Fatalf("played %v, want the resolved URL", device.played)
	}
	if device.types[0] != "audio/mpeg" {
		t.Errorf("content type = %q, want audio/mpeg", device.types[0])
	}
	if err := e.PlayStation(9); err == nil {
		t.Error("playing a station that is not there should fail")
	}
}

func TestFavorites(t *testing.T) {
	e, device, _ := newTestEngineWithSource(t)
	e.LoadStations()
	waitFor(t, func() bool { return len(e.Snapshot().Stations) == 2 })

	if err := e.AddStationFavorite(0); err != nil {
		t.Fatal(err)
	}
	if got := len(e.Snapshot().Favorites); got != 1 {
		t.Fatalf("favorites = %d", got)
	}
	fav := e.Snapshot().Favorites[0]
	if fav.Name != "Rock Antenne" || fav.URL != "https://a/stream.mp3" {
		t.Errorf("favorite = %+v", fav)
	}
	if !e.IsFavorite("https://a/stream.mp3") {
		t.Error("the saved station is not reported as a favorite")
	}

	// Adding it again does nothing rather than failing.
	if err := e.AddStationFavorite(0); err != nil {
		t.Fatalf("adding a favorite twice: %v", err)
	}
	if got := len(e.Snapshot().Favorites); got != 1 {
		t.Errorf("the duplicate was saved: %d", got)
	}

	// Playing it reaches the device.
	if err := e.PlayFavorite(0); err != nil {
		t.Fatal(err)
	}
	if len(device.played) != 1 {
		t.Fatalf("played %v", device.played)
	}

	// Removing it takes it out, and it is on disk in between.
	if err := e.RemoveFavorite(0); err != nil {
		t.Fatal(err)
	}
	if got := len(e.Snapshot().Favorites); got != 0 {
		t.Errorf("favorites left: %d", got)
	}
}

func TestFavoritesSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	source := &fakeSource{}
	makeEngine := func() *Engine {
		e, err := New(Options{DataDir: dir, Stations: source})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Close)
		return e
	}

	e := makeEngine()
	if err := e.AddFavorite(playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"}); err != nil {
		t.Fatal(err)
	}

	// A new engine over the same directory reads it back.
	again := makeEngine()
	defaults := again.Snapshot().Favorites
	// The favorite is loaded in New, before any search.
	_ = defaults
	if got := again.FavoritesPath(); got == "" {
		t.Fatal("no favorites path")
	}
	if !again.IsFavorite("http://radio1.com/1.mp3") {
		t.Error("the favorite was not read back")
	}
}

func TestToggleFavorite(t *testing.T) {
	e, _, _ := newTestEngineWithSource(t)
	e.LoadStations()
	waitFor(t, func() bool { return len(e.Snapshot().Stations) == 2 })

	if err := e.ToggleFavorite("https://a/stream.mp3"); err != nil {
		t.Fatal(err)
	}
	if !e.IsFavorite("https://a/stream.mp3") {
		t.Fatal("the toggle did not save the station")
	}
	if err := e.ToggleFavorite("https://a/stream.mp3"); err != nil {
		t.Fatal(err)
	}
	if e.IsFavorite("https://a/stream.mp3") {
		t.Error("the toggle did not take it out")
	}
	if err := e.ToggleFavorite("http://nowhere"); err == nil {
		t.Error("toggling an unknown URL should fail")
	}
}

func TestSearchErrorIsReported(t *testing.T) {
	e, _, source := newTestEngineWithSource(t)
	source.err = errors.New("the network is down")
	e.LoadStations()
	waitFor(t, func() bool { return e.Snapshot().SearchErr != "" })
	if got := e.Snapshot().SearchErr; !strings.Contains(got, "network is down") {
		t.Errorf("search error = %q", got)
	}
	if e.Snapshot().Searching {
		t.Error("the search is still reported as running")
	}
}
