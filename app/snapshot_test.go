package app

import (
	"image/png"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/free/gocc/cast"
	"github.com/free/gocc/playlist"
)

// TestRenderSnapshot writes a picture of the window to /tmp, to look at
// what the interface actually shows.
func TestRenderSnapshot(t *testing.T) {
	if os.Getenv("GOCC_SNAPSHOT") == "" {
		t.Skip("set GOCC_SNAPSHOT to write a snapshot")
	}

	a, device := newTestApp(t,
		playlist.Entry{Name: "radio1", URL: "http://radio1.com/1.mp3"},
		playlist.Entry{Name: "jazz", URL: "http://jazz.example/stream", ContentType: "audio/aac"},
		playlist.Entry{Name: "classic", URL: "http://classic.example/live"},
	)
	device.status = cast.Status{
		State:  cast.StatePlaying,
		Title:  "Morning Jazz",
		Artist: "Radio Jazz",
		URL:    "http://jazz.example/stream",
		Volume: 0.62,
	}
	// Report it the way the real client does, so that the app shows what
	// a device actually reporting this state would produce.
	device.report()
	a.volume = 0.62
	a.chosen = 1
	a.devices = append(a.devices, cast.Device{
		UUID: "uuid-2", Name: "Kitchen", Addr: "10.0.0.9", Port: 8009,
	})

	img := ui.Render(a.View, 1080, 660, 2)
	f, err := os.Create("/tmp/gocc-snapshot.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	t.Log("wrote /tmp/gocc-snapshot.png")
}
