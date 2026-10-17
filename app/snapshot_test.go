package app

import (
	"context"
	"image/png"
	"net"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/free/gocc/cast"
	"github.com/free/gocc/core"
	"github.com/free/gocc/playlist"
)

// TestRenderSnapshot writes a picture of the window to /tmp, to look at
// what the interface actually shows.
func TestRenderSnapshot(t *testing.T) {
	if os.Getenv("GOCC_SNAPSHOT") == "" {
		t.Skip("set GOCC_SNAPSHOT to write a snapshot")
	}

	device := &fakeDevice{}
	kitchen := cast.Device{UUID: "uuid-2", Name: "Kitchen", Addr: "10.0.0.9", Port: 8009}

	engine, err := core.New(core.Options{
		DataDir:  t.TempDir(),
		Stations: newTestSource(),
		NewController: func(_ cast.Device, onChange func(cast.Status, error)) (core.Controller, error) {
			device.onChange = onChange
			return device, nil
		},
		Discover: func(_ context.Context, _ *net.Interface, onDevices func([]cast.Device)) (func(), error) {
			onDevices([]cast.Device{testDevice, kitchen})
			return func() {}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	for _, entry := range []playlist.Entry{
		{Name: "radio1", URL: "http://radio1.com/1.mp3"},
		{Name: "jazz", URL: "http://jazz.example/stream", ContentType: "audio/aac"},
		{Name: "classic", URL: "http://classic.example/live"},
	} {
		if err := engine.AddEntry(entry); err != nil {
			t.Fatal(err)
		}
	}
	engine.Start(context.Background())
	engine.Choose(testDevice)
	waitFor(t, func() bool { return !engine.Snapshot().Connecting })

	// The device has just been asked for an entry and is buffering it: the
	// window should show that it is working on it.
	engine.ChooseEntry(1)
	device.quiet = true
	engine.PlayIndex(1)
	waitFor(t, func() bool { return engine.Snapshot().Loading })

	a := &App{
		engine:     engine,
		deviceSel:  0,
		chosen:     -1,
		favChosen:  0,
		stationSel: 0,
		logos:      map[string]logoState{},
	}
	a.volume, a.volumeAt = 0.62, time.Now()

	// Read the catalogue, so that a station can be saved from it.
	engine.LoadCountries()
	waitFor(t, func() bool { return len(engine.Snapshot().Stations) > 0 })

	// The favorites the window shows first.
	if err := engine.AddFavorite(playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"}); err != nil {
		t.Fatal(err)
	}
	if err := engine.AddStationFavorite(0); err != nil {
		t.Fatal(err)
	}
	render(t, a, "/tmp/gocc-snapshot-favorites.png")

	// The browse pane, with the catalogue read.
	a.pane = paneBrowse
	a.countriesAsked = true
	render(t, a, "/tmp/gocc-snapshot-browse.png")
}

// render draws the window once and writes it as a PNG.
func render(t *testing.T, a *App, path string) {
	t.Helper()
	img := ui.Render(a.View, 1080, 660, 2)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	t.Log("wrote " + path)
}
