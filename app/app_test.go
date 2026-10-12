package app

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/free/gocc/cast"
	"github.com/free/gocc/core"
	"github.com/free/gocc/playlist"
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
}

func (f *fakeDevice) report() {
	if f.onChange != nil {
		f.onChange(f.status, nil)
	}
}

func (f *fakeDevice) Status() cast.Status { return f.status }

func (f *fakeDevice) Play(url, contentType string) {
	f.played = append(f.played, url)
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

// newTestApp makes the window with a playlist in a temporary directory and
// a device already connected, without touching the network.
func newTestApp(t *testing.T, entries ...playlist.Entry) (*App, *fakeDevice) {
	t.Helper()
	device := &fakeDevice{}

	engine, err := core.New(core.Options{
		DataDir: t.TempDir(),
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

	a := &App{engine: engine, deviceSel: -1}
	for _, entry := range entries {
		if err := engine.AddEntry(entry); err != nil {
			t.Fatal(err)
		}
	}

	engine.Start(context.Background())
	engine.Choose(testDevice)
	waitFor(t, func() bool {
		s := engine.Snapshot()
		return s.Selected == testDevice.UUID && !s.Connecting
	})
	return a, device
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

func TestPlaylistDoubleClickPlays(t *testing.T) {
	a, device := newTestApp(t,
		playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"},
		playlist.Entry{Name: "radio2", URL: "http://radio2.com/2.mp3"},
	)
	tt := ui.NewTester(a.View, 1100, 700)

	if !tt.HasText("radio1") || !tt.HasText("radio2") {
		t.Fatalf("the playlist does not show: %v", tt.Texts())
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

	// Nothing plays, so the button offers to play the chosen entry.
	if err := tt.Click("radio1"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Play"); err != nil {
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

func TestAddEntry(t *testing.T) {
	a, _ := newTestApp(t)
	tt := ui.NewTester(a.View, 1100, 700)

	if !tt.HasText("Add an entry") {
		t.Fatalf("no form: %v", tt.Texts())
	}

	a.newName = "radio3"
	a.newURL = "http://radio3.com/3.mp3"
	tt.Frame()
	if err := tt.Click("Add"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("radio3") {
		t.Errorf("the new entry does not show: %v", tt.Texts())
	}

	// It is on disk, so that the next run finds it.
	again, err := playlist.Load(a.engine.PlaylistPath())
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

func TestAddEntryRejectsBadURL(t *testing.T) {
	a, _ := newTestApp(t)
	tt := ui.NewTester(a.View, 1100, 700)

	a.newName = "bad"
	a.newURL = "not a url"
	tt.Frame()
	if err := tt.Click("Add"); err != nil {
		t.Fatal(err)
	}
	if got := a.engine.Snapshot().Playlist; len(got) != 0 {
		t.Fatalf("a bad entry was added: %+v", got)
	}
	tt.Frame()
	if !tt.HasText("Last error") {
		t.Errorf("the error is not shown: %v", tt.Texts())
	}
}

func TestRemoveAndMove(t *testing.T) {
	a, _ := newTestApp(t,
		playlist.Entry{Name: "a", URL: "http://x.com/a"},
		playlist.Entry{Name: "b", URL: "http://x.com/b"},
		playlist.Entry{Name: "c", URL: "http://x.com/c"},
	)
	tt := ui.NewTester(a.View, 1100, 700)

	if err := tt.Click("b"); err != nil {
		t.Fatal(err)
	}
	if got := a.engine.Snapshot().Chosen; got != 1 {
		t.Fatalf("chosen = %d, want 1", got)
	}
	if err := tt.Click("Down"); err != nil {
		t.Fatal(err)
	}
	if names := entryNames(a); names != "a,c,b" {
		t.Fatalf("after Down: %v", names)
	}
	if got := a.engine.Snapshot().Chosen; got != 2 {
		t.Errorf("the moved entry is not still chosen: %d", got)
	}
	if err := tt.Click("Remove"); err != nil {
		t.Fatal(err)
	}
	if names := entryNames(a); names != "a,c" {
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
	a.engine.PlayChosen()
	if a.engine.Snapshot().Err == "" {
		t.Fatal("no error with no device")
	}
	tt.Frame()
	if !tt.HasText("Last error") {
		t.Errorf("the error is not shown: %v", tt.Texts())
	}
}

func entryNames(a *App) string {
	names := ""
	for i, entry := range a.engine.Snapshot().Playlist {
		if i > 0 {
			names += ","
		}
		names += entry.Name
	}
	return names
}
