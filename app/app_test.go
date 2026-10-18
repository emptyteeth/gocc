package app

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/emptyteeth/gocc/cast"
	"github.com/emptyteeth/gocc/core"
	"github.com/emptyteeth/gocc/playlist"
	"github.com/emptyteeth/gocc/radiobrowser"
)

// fakeDevice stands in for a device, so that the window can be driven in a
// test without one on the network. It reports its status through the same
// callback the engine hands to a real client.
type fakeDevice struct {
	status   cast.Status
	onChange func(cast.Status, error)
	played   []string
	volume   []float64
	muted    []bool
	paused   int
	resumed  int
	stopped  int
	// quiet makes Play record the request without reporting that the
	// device plays it, as a real device does while it buffers.
	quiet bool
}

func (f *fakeDevice) report() {
	if f.onChange != nil {
		f.onChange(f.status, nil)
	}
}

func (f *fakeDevice) Status() cast.Status { return f.status }

func (f *fakeDevice) Play(url, contentType string) {
	f.played = append(f.played, url)
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
func (f *fakeDevice) Close()   {}

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

var testDevice = cast.Device{
	UUID: "uuid-1", Name: "Living Room", Addr: "10.0.0.5", Port: 8009,
}

// newTestApp makes the window with the entries saved as favorites, a
// catalogue to browse, and a device already connected, without touching
// the network.
func newTestApp(t *testing.T, entries ...playlist.Entry) (*App, *fakeDevice) {
	t.Helper()
	a, device, _ := newTestAppWithSource(t, entries...)
	return a, device
}

// newTestAppWithSource is newTestApp, and returns the catalogue the tests
// can look at.
func newTestAppWithSource(t *testing.T, entries ...playlist.Entry) (*App, *fakeDevice, *fakeSource) {
	t.Helper()
	device := &fakeDevice{}
	source := newTestSource()

	engine, err := core.New(core.Options{
		DataDir:  t.TempDir(),
		Stations: source,
		// The device is handed the callback the engine would give a real
		// client, so that it reports status the same way.
		NewController: func(_ cast.Device, onChange func(cast.Status, error)) (core.Controller, error) {
			device.onChange = onChange
			return device, nil
		},
		// Discovery reports the one device, without mDNS or a network.
		Discover: func(_ context.Context, _ *net.Interface, onDevices func([]cast.Device)) (func(), error) {
			onDevices([]cast.Device{testDevice})
			return func() {}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)

	a := &App{
		engine:     engine,
		deviceSel:  -1,
		chosen:     -1,
		favChosen:  -1,
		stationSel: -1,
		logos:      map[string]logoState{},
	}
	for _, entry := range entries {
		if err := engine.AddFavorite(entry); err != nil {
			t.Fatal(err)
		}
	}

	engine.Start(context.Background())
	engine.Choose(testDevice)
	waitFor(t, func() bool {
		s := engine.Snapshot()
		return s.Selected == testDevice.UUID && !s.Connecting
	})
	return a, device, source
}

// newTestSource is a catalogue for the tests: two countries and a few
// stations, answered without a network.
func newTestSource() *fakeSource {
	return &fakeSource{
		countries: []radiobrowser.Country{
			{Name: "Germany", Code: "DE", Stations: 6533},
			{Name: "France", Code: "FR", Stations: 3926},
		},
		stations: []radiobrowser.Station{
			{UUID: "s1", Name: "Rock Antenne", URL: "http://a",
				URLResolved: "https://a/stream.mp3", Codec: "MP3", CountryCode: "DE", Bitrate: 128},
			{UUID: "s2", Name: "Jazz Radio", URL: "http://b",
				URLResolved: "https://b/stream", Codec: "AAC", CountryCode: "DE"},
			{UUID: "s3", Name: "France Inter", URL: "http://c",
				URLResolved: "https://c/stream.mp3", Codec: "MP3", CountryCode: "FR"},
		},
	}
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

func TestFavoritesDoubleClickPlays(t *testing.T) {
	a, device := newTestApp(t,
		playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"},
		playlist.Entry{Name: "radio2", URL: "http://radio2.com/2.mp3"},
	)
	tt := ui.NewTester(a.View, 1100, 700)

	if !tt.HasText("radio1") || !tt.HasText("radio2") {
		t.Fatalf("the favorites do not show: %v", tt.Texts())
	}

	// The first click chooses the entry, and plays nothing.
	if err := tt.Click("radio1"); err != nil {
		t.Fatal(err)
	}
	if len(device.played) != 0 {
		t.Fatalf("a single click played %v", device.played)
	}

	// A second click on the same row is a double click, and plays it.
	r, ok := tt.Find("radio1")
	if !ok {
		t.Fatal("the row is gone")
	}
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	if len(device.played) != 1 || device.played[0] != "http://radio1.com/1.mp3" {
		t.Fatalf("played %v, want the first URL", device.played)
	}

	// The second entry plays too.
	if err := tt.Click("radio2"); err != nil {
		t.Fatal(err)
	}
	r, _ = tt.Find("radio2")
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	if len(device.played) != 2 || device.played[1] != "http://radio2.com/2.mp3" {
		t.Fatalf("played %v, want the second URL too", device.played)
	}
}

func TestTransportButtons(t *testing.T) {
	a, device := newTestApp(t, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})
	tt := ui.NewTester(a.View, 1100, 700)

	// Nothing plays, so the button offers to play the chosen favorite.
	if err := tt.Click("radio1"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Play favorite"); err != nil {
		t.Fatal(err)
	}
	if len(device.played) != 1 {
		t.Fatalf("Play played %v", device.played)
	}

	// Once it plays, the button pauses.
	device.status.State = cast.StatePlaying
	device.report()
	tt.Frame()
	if !tt.HasText("Pause") {
		t.Fatalf("the button did not become Pause: %v", tt.Texts())
	}
	if err := tt.Click("Pause"); err != nil {
		t.Fatal(err)
	}
	if device.paused != 1 {
		t.Fatalf("paused %d times", device.paused)
	}

	if err := tt.Click("Stop"); err != nil {
		t.Fatal(err)
	}
	if device.stopped != 1 {
		t.Fatalf("stopped %d times", device.stopped)
	}
}

func TestVolumeSliderSendsNothingWhenItDoesNotMove(t *testing.T) {
	a, device := newTestApp(t)
	tt := ui.NewTester(a.View, 1100, 700)

	// Redrawing the window is not an adjustment: a device that answers
	// every command with a sound must not be told anything.
	for range 5 {
		tt.Frame()
	}
	if len(device.volume) != 0 {
		t.Fatalf("redrawing sent %v", device.volume)
	}

	// Dragging the slider is, and the level reaches the device.
	r, ok := tt.Find("Volume")
	if !ok {
		t.Fatalf("no volume slider: %v", tt.Texts())
	}
	tt.Press(r.X+r.W/2, r.Y+r.H/2)
	tt.Move(r.X+r.W*0.75, r.Y+r.H/2)
	tt.Release(r.X+r.W*0.75, r.Y+r.H/2)
	if len(device.volume) == 0 {
		t.Fatal("the drag told the device nothing")
	}
	if got := device.volume[len(device.volume)-1]; got < 0.6 || got > 0.9 {
		t.Errorf("the slider set the volume to %v, want about 0.75", got)
	}
}

func TestAddFavoriteByURL(t *testing.T) {
	a, _ := newTestApp(t)
	tt := ui.NewTester(a.View, 1100, 700)

	// The form is a row: a name field, a URL field and Add.
	for _, label := range []string{"Name", "URL", "Add"} {
		if !found(tt, label) {
			t.Fatalf("the form has no %s: %v", label, tt.Texts())
		}
	}
	// They share a row: their tops are close together.
	name, _ := tt.Find("Name")
	url, _ := tt.Find("URL")
	add, _ := tt.Find("Add")
	if d := url.Y - name.Y; d > 12 || d < -12 {
		t.Errorf("the name and URL are not on one row: %v and %v", name.Y, url.Y)
	}
	if d := add.Y - name.Y; d > 12 || d < -12 {
		t.Errorf("the Add button is not on the form's row: %v and %v", add.Y, name.Y)
	}

	a.newName = "radio3"
	a.newURL = "http://radio3.com/3.mp3"
	tt.Frame()
	if err := tt.Click("Add"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("radio3") {
		t.Errorf("the new favorite does not show: %v", tt.Texts())
	}

	// It is on disk, so that the next run finds it.
	again, err := playlist.Load(a.engine.FavoritesPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Entries(); len(got) != 1 || got[0].URL != "http://radio3.com/3.mp3" {
		t.Fatalf("saved %+v", got)
	}

	// The form is empty again, so that the next entry can be typed.
	if a.newName != "" || a.newURL != "" {
		t.Errorf("the form still holds %q, %q", a.newName, a.newURL)
	}
}

func TestAddFavoriteRejectsBadURL(t *testing.T) {
	a, _ := newTestApp(t)
	tt := ui.NewTester(a.View, 1100, 700)

	a.newName = "bad"
	a.newURL = "not a url"
	tt.Frame()
	if err := tt.Click("Add"); err != nil {
		t.Fatal(err)
	}
	if got := a.engine.Snapshot().Favorites; len(got) != 0 {
		t.Fatalf("a bad entry was added: %+v", got)
	}
	tt.Frame()
	if !tt.HasText("Last error") {
		t.Errorf("the error is not shown: %v", tt.Texts())
	}
}

func TestRemoveFavorite(t *testing.T) {
	a, _ := newTestApp(t,
		playlist.Entry{Name: "a", URL: "http://x.com/a"},
		playlist.Entry{Name: "b", URL: "http://x.com/b"},
	)
	tt := ui.NewTester(a.View, 1100, 700)

	if err := tt.Click("b"); err != nil {
		t.Fatal(err)
	}
	if a.favChosen != 1 {
		t.Fatalf("chosen = %d, want 1", a.favChosen)
	}
	if err := tt.Click("Remove"); err != nil {
		t.Fatal(err)
	}
	if names := favoriteNames(a); names != "a" {
		t.Fatalf("after Remove: %v", names)
	}
}

func TestDisconnectedDeviceReports(t *testing.T) {
	a, _ := newTestApp(t, playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"})
	a.engine.Disconnect()
	tt := ui.NewTester(a.View, 1100, 700)

	// Playing without a device says so rather than doing nothing.
	if err := tt.Click("radio1"); err != nil {
		t.Fatal(err)
	}
	if err := a.engine.PlayFavorite(a.favChosen); err != nil {
		t.Fatal(err)
	}
	if a.engine.Snapshot().Err == "" {
		t.Fatal("no error with no device")
	}
	tt.Frame()
	if !tt.HasText("Last error") {
		t.Errorf("the error is not shown: %v", tt.Texts())
	}
}

func favoriteNames(a *App) string {
	names := ""
	for i, entry := range a.engine.Snapshot().Favorites {
		if i > 0 {
			names += ","
		}
		names += entry.Name
	}
	return names
}

// fakeSource is a radio-browser catalogue that answers without a network.
type fakeSource struct {
	mu        sync.Mutex
	countries []radiobrowser.Country
	stations  []radiobrowser.Station
	queries   []radiobrowser.Query
}

func (f *fakeSource) Countries(context.Context) ([]radiobrowser.Country, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]radiobrowser.Country(nil), f.countries...), nil
}

func (f *fakeSource) Stations(_ context.Context, q radiobrowser.Query) ([]radiobrowser.Station, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	out := make([]radiobrowser.Station, 0, len(f.stations))
	for _, station := range f.stations {
		if q.CountryCode != "" && station.CountryCode != q.CountryCode {
			continue
		}
		if q.Name != "" && !strings.Contains(strings.ToLower(station.Name), strings.ToLower(q.Name)) {
			continue
		}
		out = append(out, station)
	}
	return out, nil
}

func (f *fakeSource) asked() []radiobrowser.Query {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]radiobrowser.Query(nil), f.queries...)
}

func TestBrowseShowsStationsAndFavorites(t *testing.T) {
	a, device, source := newTestAppWithSource(t)
	tt := ui.NewTester(a.View, 1200, 720)

	// The favorites pane is what shows first, with its add form.
	if !tt.HasText("Favorites") || !found(tt, "Name") {
		t.Fatalf("the favorites pane does not show: %v", tt.Texts())
	}

	// Browse reads the catalogue, with the first country chosen.
	if err := tt.Click("Browse"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(a.engine.Snapshot().Countries) == 2 })
	waitFor(t, func() bool { return len(a.engine.Snapshot().Stations) > 0 })
	tt.Frame()

	if !tt.HasText("Rock Antenne") {
		t.Fatalf("the stations do not show: %v", tt.Texts())
	}
	if got := a.engine.Snapshot().CountryCode; got != "DE" {
		t.Errorf("country = %q, want the first, DE", got)
	}
	// The catalogue was asked for Germany.
	queries := source.asked()
	if len(queries) == 0 || queries[len(queries)-1].CountryCode != "DE" {
		t.Errorf("queries = %+v", queries)
	}

	// The card's buttons float over the logo while the pointer is on it.
	// Move the pointer there, as a hand would.
	hover(t, tt, "Rock Antenne")

	// The card's Play button plays that station's resolved URL.
	if err := tt.Click("Play Rock Antenne"); err != nil {
		t.Fatal(err)
	}
	if len(device.played) != 1 || device.played[0] != "https://a/stream.mp3" {
		t.Fatalf("played %v", device.played)
	}

	// Saving it adds it to the favorites, and the card says so.
	if err := tt.Click("Save Rock Antenne"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(a.engine.Snapshot().Favorites) == 1 })
	tt.Frame()
	if !tt.HasText("Saved Rock Antenne") {
		t.Errorf("the card does not show the station as saved: %v", tt.Texts())
	}

	// The favorites pane holds it.
	if err := tt.Click("Favorites"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Rock Antenne") {
		t.Errorf("the saved station is not in the favorites: %v", tt.Texts())
	}
	if names := favoriteNames(a); names != "Rock Antenne" {
		t.Errorf("favorites = %v", names)
	}
}

func TestBrowseChangesCountryAndOrder(t *testing.T) {
	a, _, source := newTestAppWithSource(t)
	tt := ui.NewTester(a.View, 1200, 720)

	if err := tt.Click("Browse"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(a.engine.Snapshot().Countries) == 2 })

	// Choosing France asks for France.
	a.engine.SetCountry("FR")
	waitFor(t, func() bool {
		queries := source.asked()
		return len(queries) > 0 && queries[len(queries)-1].CountryCode == "FR"
	})

	// The order is part of the next request.
	a.engine.SetOrder(radiobrowser.OrderName)
	waitFor(t, func() bool {
		queries := source.asked()
		return len(queries) > 0 && queries[len(queries)-1].Order == radiobrowser.OrderName
	})
	tt.Frame()
	for _, label := range []string{"Votes", "Popular", "Name"} {
		if !tt.HasText(label) {
			t.Errorf("the order control does not show %q: %v", label, tt.Texts())
		}
	}
	if tt.HasText("Bitrate") {
		t.Errorf("the order control still offers Bitrate: %v", tt.Texts())
	}
}

func TestBrowseSearch(t *testing.T) {
	a, _, source := newTestAppWithSource(t)
	tt := ui.NewTester(a.View, 1200, 720)

	if err := tt.Click("Browse"); err != nil {
		t.Fatal(err)
	}
	// The pane reads the countries on its first frame, then the stations
	// of the first country.
	tt.Frame()
	waitFor(t, func() bool { return len(a.engine.Snapshot().Countries) > 0 })
	waitFor(t, func() bool { return len(a.engine.Snapshot().Stations) > 0 })

	// Typing in the search field, then waiting for the pause, searches.
	tt.Click("Search stations")
	tt.Type("jazz")
	// The tester's clock only Advance moves, so the 300ms pause is reached
	// by advancing it, as time passes in a real window.
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	waitFor(t, func() bool {
		queries := source.asked()
		return len(queries) > 0 && queries[len(queries)-1].Name == "jazz"
	})
	waitFor(t, func() bool { return len(a.engine.Snapshot().Stations) == 1 })
	tt.Frame()
	if !tt.HasText("Jazz Radio") || tt.HasText("Rock Antenne") {
		t.Errorf("the search result does not show: %v", tt.Texts())
	}
}

// found reports whether an element with this text or label is shown.
func found(tt *ui.Tester, text string) bool {
	_, ok := tt.Find(text)
	return ok
}

// hover puts the pointer on the card showing text, as a hand would.
func hover(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	r, ok := tt.Find(text)
	if !ok {
		t.Fatalf("no card shows %q: %v", text, tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
}

func TestCardButtonsAppearOnlyOnHover(t *testing.T) {
	a, _, _ := newTestAppWithSource(t)
	tt := ui.NewTester(a.View, 1200, 720)

	if err := tt.Click("Browse"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitFor(t, func() bool { return len(a.engine.Snapshot().Stations) > 0 })
	tt.Frame()

	// Away from the cards, the cards are pictures and text only.
	tt.Move(0, 0)
	tt.Frame()
	if found(tt, "Play Rock Antenne") {
		t.Error("the Play button shows without the pointer on the card")
	}
	if found(tt, "Save Rock Antenne") {
		t.Error("the Save button shows without the pointer on the card")
	}

	// On the card, they float over its logo.
	hover(t, tt, "Rock Antenne")
	if !found(tt, "Play Rock Antenne") || !found(tt, "Save Rock Antenne") {
		t.Fatalf("the buttons do not show on hover: %v", tt.Texts())
	}

	// Leaving the card hides them again.
	tt.Move(0, 0)
	tt.Frame()
	if found(tt, "Play Rock Antenne") {
		t.Error("the Play button stayed after the pointer left")
	}
}

func TestCardButtonsShowOnTheChosenCard(t *testing.T) {
	// The keyboard cannot hover, so the card it chose shows its buttons.
	a, _, _ := newTestAppWithSource(t)
	tt := ui.NewTester(a.View, 1200, 720)

	if err := tt.Click("Browse"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitFor(t, func() bool { return len(a.engine.Snapshot().Stations) > 0 })
	tt.Frame()

	a.stationSel = 0
	tt.Frame()
	if !found(tt, "Play Rock Antenne") {
		t.Fatalf("the chosen card does not show its buttons: %v", tt.Texts())
	}
}

func TestSavedCardShowsAFilledHeart(t *testing.T) {
	a, _, _ := newTestAppWithSource(t)
	tt := ui.NewTester(a.View, 1200, 720)

	if err := tt.Click("Browse"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitFor(t, func() bool { return len(a.engine.Snapshot().Stations) > 0 })
	tt.Frame()

	hover(t, tt, "Rock Antenne")
	if err := tt.Click("Save Rock Antenne"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return a.engine.IsFavorite("https://a/stream.mp3") })
	tt.Frame()

	// The button names the station as saved, and the card marks it.
	if !found(tt, "Saved Rock Antenne") {
		t.Errorf("the save button does not say the station is saved: %v", tt.Texts())
	}
	if !tt.HasText("♥") {
		t.Errorf("the card does not show the saved mark: %v", tt.Texts())
	}
}

func TestSearchFieldIsOnBothPanes(t *testing.T) {
	a, _, _ := newTestAppWithSource(t,
		playlist.Entry{Name: "Jazz FM", URL: "http://jazz.example/1.mp3"},
		playlist.Entry{Name: "Rock Radio", URL: "http://rock.example/2.mp3"},
	)
	tt := ui.NewTester(a.View, 1200, 720)

	// The favorites pane has the search field at the top.
	if !found(tt, "Search favorites") {
		t.Fatalf("no search field on the favorites: %v", tt.Texts())
	}

	// So does the catalogue.
	if err := tt.Click("Browse"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !found(tt, "Search stations") {
		t.Fatalf("no search field on the catalogue: %v", tt.Texts())
	}

	// The field is at the top row, to the right of the segmented control.
	seg, _ := tt.Find("Browse")
	field, _ := tt.Find("Search stations")
	if field.Y > seg.Y+seg.H+8 {
		t.Errorf("the search field is not on the tab row: tab Y=%v field Y=%v", seg.Y, field.Y)
	}
	if field.X <= seg.X {
		t.Errorf("the search field is not to the right of the tabs: tab X=%v field X=%v", seg.X, field.X)
	}
}

func TestFavoritesSearchNarrowsTheList(t *testing.T) {
	a, _, _ := newTestAppWithSource(t,
		playlist.Entry{Name: "Jazz FM", URL: "http://jazz.example/1.mp3"},
		playlist.Entry{Name: "Rock Radio", URL: "http://rock.example/2.mp3"},
	)
	tt := ui.NewTester(a.View, 1200, 720)

	if !tt.HasText("Jazz FM") || !tt.HasText("Rock Radio") {
		t.Fatalf("the favorites do not show: %v", tt.Texts())
	}

	tt.Click("Search favorites")
	tt.Type("jazz")
	tt.Frame()

	if !tt.HasText("Jazz FM") {
		t.Errorf("the matching favorite is gone: %v", tt.Texts())
	}
	if tt.HasText("Rock Radio") {
		t.Errorf("the non-matching favorite still shows: %v", tt.Texts())
	}

	// Clearing the field brings them back.
	tt.Key(ui.Cmd, ui.KeyA)
	tt.TypeKey(0, ui.KeyBackspace, "")
	tt.Frame()
	if !tt.HasText("Rock Radio") {
		t.Errorf("clearing the search did not restore the list: %v", tt.Texts())
	}
}

func TestFavoritesSearchPlaysTheRightRow(t *testing.T) {
	a, device, _ := newTestAppWithSource(t,
		playlist.Entry{Name: "Jazz FM", URL: "http://jazz.example/1.mp3"},
		playlist.Entry{Name: "Rock Radio", URL: "http://rock.example/2.mp3"},
	)
	tt := ui.NewTester(a.View, 1200, 720)

	tt.Click("Search favorites")
	tt.Type("rock")
	tt.Frame()
	if !tt.HasText("Rock Radio") || tt.HasText("Jazz FM") {
		t.Fatalf("the filter did not work: %v", tt.Texts())
	}

	// The only row left is the second favorite; playing it must use the
	// URL of the whole list's second entry, not the filtered first.
	if err := tt.Click("Rock Radio"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Play favorite"); err != nil {
		t.Fatal(err)
	}
	if len(device.played) != 1 || device.played[0] != "http://rock.example/2.mp3" {
		t.Fatalf("played %v, want the Rock Radio URL", device.played)
	}
}

func TestBrowseCountryOrderRefreshShareARow(t *testing.T) {
	a, _, _ := newTestAppWithSource(t)
	tt := ui.NewTester(a.View, 1400, 800)

	if err := tt.Click("Browse"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitFor(t, func() bool { return len(a.engine.Snapshot().Countries) > 0 })
	tt.Frame()

	country, ok := tt.Find("Country")
	if !ok {
		t.Fatalf("no country picker: %v", tt.Texts())
	}
	votes, _ := tt.Find("Votes")
	refresh, _ := tt.Find("Refresh catalogue")
	for _, tc := range []struct {
		name string
		y    float32
	}{{"Votes", votes.Y}, {"Refresh", refresh.Y}} {
		if d := tc.y - country.Y; d > 24 || d < -24 {
			t.Errorf("%s is not on the country's row: country Y=%v %s Y=%v",
				tc.name, country.Y, tc.name, tc.y)
		}
	}
}
