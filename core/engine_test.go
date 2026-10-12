package core

import (
	"testing"
	"time"

	"github.com/free/gocc/cast"
	"github.com/free/gocc/playlist"
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
