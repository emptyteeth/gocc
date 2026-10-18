package radiobrowser

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emptyteeth/gocc/playlist"
)

// serve makes a client whose base URL is a test server answering with the
// handler.
func serve(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Options{BaseURL: srv.URL, UserAgent: "gocc-test/1"})
}

func TestCountries(t *testing.T) {
	var gotQuery string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if ua := r.Header.Get("User-Agent"); ua != "gocc-test/1" {
			t.Errorf("user agent = %q", ua)
		}
		w.Header().Set("Content-Type", "application/json")
		// Some mirrors send the count as a string, some as a number.
		w.Write([]byte(`[
			{"name":"Germany","iso_3166_1":"DE","stationcount":6533},
			{"name":"France","iso_3166_1":"FR","stationcount":"3926"},
			{"name":"","iso_3166_1":"XX","stationcount":1}
		]`))
	})

	countries, err := c.Countries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(countries) != 2 {
		t.Fatalf("got %d countries, want 2 (one has no name)", len(countries))
	}
	if countries[0].Name != "Germany" || countries[0].Code != "DE" || countries[0].Stations != 6533 {
		t.Errorf("first = %+v", countries[0])
	}
	if countries[1].Stations != 3926 {
		t.Errorf("a count sent as a string was read as %d", countries[1].Stations)
	}
	if !strings.Contains(gotQuery, "order=stationcount") || !strings.Contains(gotQuery, "reverse=true") {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestStationsQuery(t *testing.T) {
	var gotQuery string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`[{
			"stationuuid":"u1","name":"  Rock   Antenne ",
			"url":"http://a/stream","url_resolved":"https://a/stream.mp3",
			"favicon":"https://a/logo.png","country":"Germany","countrycode":"DE",
			"codec":"MP3","bitrate":128,"votes":10,"clickcount":3,"hls":0
		}]`))
	})

	stations, err := c.Stations(context.Background(), Query{
		CountryCode: "de",
		Order:       OrderBitrate,
		Reverse:     true,
		Name:        "rock",
		Limit:       25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stations) != 1 {
		t.Fatalf("got %d stations", len(stations))
	}
	s := stations[0]
	if s.UUID != "u1" || s.URLResolved != "https://a/stream.mp3" {
		t.Errorf("station = %+v", s)
	}
	for _, want := range []string{
		"countrycode=DE", "order=bitrate", "reverse=true",
		"name=rock", "limit=25", "hidebroken=true",
	} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q lacks %q", gotQuery, want)
		}
	}
}

func TestStationsDefaults(t *testing.T) {
	var gotQuery string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`[]`))
	})
	if _, err := c.Stations(context.Background(), Query{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"order=votes", "limit=100", "hidebroken=true"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q lacks %q", gotQuery, want)
		}
	}
	if strings.Contains(gotQuery, "countrycode=") || strings.Contains(gotQuery, "name=") {
		t.Errorf("an empty filter was sent: %q", gotQuery)
	}
}

func TestStationsRejectsAnUnknownOrder(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the server should not have been asked")
	})
	if _, err := c.Stations(context.Background(), Query{Order: "sideways"}); err == nil {
		t.Error("an unknown order was accepted")
	}
}

func TestErrorStatus(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	})
	_, err := c.Stations(context.Background(), Query{})
	if err == nil {
		t.Fatal("no error for a 503")
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error is %T, want *Error", err)
	}
	if e.Status != http.StatusServiceUnavailable || e.Body != "nope" {
		t.Errorf("error = %+v", e)
	}
}

func TestBadJSON(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"not":"a list"}`))
	})
	if _, err := c.Stations(context.Background(), Query{}); err == nil {
		t.Error("bad JSON was accepted")
	}
}

func TestContextCancel(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Stations(ctx, Query{}); err == nil {
		t.Error("a canceled request returned no error")
	}
}

func TestStationPlayURL(t *testing.T) {
	for _, tc := range []struct {
		station Station
		want    string
	}{
		{Station{URL: "http://a", URLResolved: "https://b"}, "https://b"},
		{Station{URL: "http://a"}, "http://a"},
		{Station{}, ""},
	} {
		if got := tc.station.URLToPlay(); got != tc.want {
			t.Errorf("URLToPlay = %q, want %q", got, tc.want)
		}
	}
}

func TestStationContentType(t *testing.T) {
	for _, tc := range []struct {
		station Station
		want    string
	}{
		{Station{Codec: "MP3"}, "audio/mpeg"},
		{Station{Codec: "mp3"}, "audio/mpeg"},
		{Station{Codec: "AAC"}, "audio/aac"},
		{Station{Codec: "AAC+"}, "audio/aac"},
		{Station{Codec: "OGG"}, "audio/ogg"},
		{Station{Codec: "FLAC"}, "audio/flac"},
		{Station{Codec: "UNKNOWN"}, ""},
		{Station{Codec: ""}, ""},
		// HLS wins over whatever codec was recorded.
		{Station{Codec: "MP3", HLS: 1}, "application/x-mpegurl"},
	} {
		if got := tc.station.ContentType(); got != tc.want {
			t.Errorf("ContentType(%+v) = %q, want %q", tc.station, got, tc.want)
		}
	}
}

func TestCleanName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  Rock   Antenne ", "Rock Antenne"},
		{"a\tb\nc", "a b c"},
		{"", ""},
		{"   ", ""},
		{"电台 一", "电台 一"},
	} {
		if got := CleanName(tc.in); got != tc.want {
			t.Errorf("CleanName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStationEntry(t *testing.T) {
	s := Station{
		Name:        "  SWR3 \n",
		URL:         "http://a",
		URLResolved: "https://b/stream.mp3",
		Codec:       "MP3",
	}
	e := s.Entry()
	if e.Name != "SWR3" || e.URL != "https://b/stream.mp3" || e.ContentType != "audio/mpeg" {
		t.Errorf("entry = %+v", e)
	}
	// The playlist refuses a name with a tab or newline, and a URL with a
	// space: what the catalogue sends must be cleaned before it is saved.
	p, err := playlist.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(e); err != nil {
		t.Errorf("the entry the catalogue makes is not one the playlist accepts: %v", err)
	}
}
