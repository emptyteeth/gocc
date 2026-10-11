package cast

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContentTypeFromPath(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want string
	}{
		{"http://radio1.com/1.mp3", "audio/mpeg"},
		{"http://radio1.com/1.MP3", "audio/mpeg"},
		{"http://radio1.com/stream.aac", "audio/aac"},
		{"http://radio1.com/stream.flac", "audio/flac"},
		{"http://radio1.com/stream.ogg", "audio/ogg"},
		{"http://radio1.com/1.mp3?token=abc", "audio/mpeg"},
		{"http://radio1.com/1.mp3#frag", "audio/mpeg"},
		{"http://radio1.com/live.m3u8", "application/x-mpegurl"},
		// A path with no extension says nothing, and must not be guessed.
		{"http://radio1.com/stream", ""},
		{"http://radio1.com/", ""},
		// A type this app does not play is not passed on as if it were.
		{"http://radio1.com/page.html", ""},
	} {
		if got := contentTypeFromPath(tc.url); got != tc.want {
			t.Errorf("contentTypeFromPath(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestContentTypePrefersThePath(t *testing.T) {
	// A server that answers something else must not be asked at all when
	// the path already says what the stream is: a radio stream often
	// refuses a HEAD request, and waiting for it would be a waste.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "audio/aac")
	}))
	defer srv.Close()

	got := ContentType(context.Background(), srv.URL+"/stream.mp3")
	if got != "audio/mpeg" {
		t.Errorf("got %q, want audio/mpeg from the path", got)
	}
	if hits != 0 {
		t.Errorf("the server was asked %d times, want none", hits)
	}
}

func TestContentTypeAsksWhenThePathSaysNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s, want HEAD", r.Method)
		}
		w.Header().Set("Content-Type", "audio/mpeg; charset=utf-8")
	}))
	defer srv.Close()

	if got := ContentType(context.Background(), srv.URL+"/stream"); got != "audio/mpeg" {
		t.Errorf("got %q, want audio/mpeg", got)
	}
}

func TestContentTypeIgnoresWhatIsNotMedia(t *testing.T) {
	// A server answering a web page must not have that passed on as the
	// media type: the device guesses better by itself.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
	}))
	defer srv.Close()

	if got := ContentType(context.Background(), srv.URL+"/stream"); got != "" {
		t.Errorf("got %q, want an empty type", got)
	}
}

func TestContentTypeSurvivesAnUnreachableURL(t *testing.T) {
	// Nothing listens here, so the request fails; that must be an empty
	// answer, not a hang and not a panic.
	if got := ContentType(context.Background(), "http://127.0.0.1:1/stream"); got != "" {
		t.Errorf("got %q, want an empty type", got)
	}
}

func TestInterfacesFindsSomething(t *testing.T) {
	// The machine running the tests has a network interface, and the
	// discovery of a device depends on finding it.
	ifaces, err := Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			t.Errorf("interface %s is not up", iface.Name)
		}
	}
}
