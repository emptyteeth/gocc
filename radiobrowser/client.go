// Package radiobrowser reads the stations of radio-browser.info, a
// community catalogue of internet radio stations.
//
// The service answers JSON over HTTP and HTTPS; this client uses HTTPS.
// Its API asks for a descriptive User-Agent, which Options sets.
package radiobrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/free/gocc/playlist"
)

// DefaultBaseURL is the host the client talks to when Options names none.
// The service publishes several; de1 is the one that answers HTTPS in
// practice.
const DefaultBaseURL = "https://de1.api.radio-browser.info"

// Order is how a search sorts its stations.
type Order string

// The orders the app offers, most useful first.
const (
	OrderVotes      Order = "votes"
	OrderClickCount Order = "clickcount"
	OrderBitrate    Order = "bitrate"
	OrderName       Order = "name"
)

// Valid reports whether o is one of the orders the service knows.
func (o Order) Valid() bool {
	switch o {
	case OrderVotes, OrderClickCount, OrderBitrate, OrderName:
		return true
	}
	return false
}

// Country is a country the catalogue has stations in.
type Country struct {
	Name     string
	Code     string
	Stations int
}

// Station is one station of the catalogue.
type Station struct {
	UUID        string `json:"stationuuid"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	URLResolved string `json:"url_resolved"`
	Homepage    string `json:"homepage"`
	Favicon     string `json:"favicon"`
	Country     string `json:"country"`
	CountryCode string `json:"countrycode"`
	State       string `json:"state"`
	Tags        string `json:"tags"`
	Codec       string `json:"codec"`
	Bitrate     int    `json:"bitrate"`
	Votes       int    `json:"votes"`
	ClickCount  int    `json:"clickcount"`
	HLS         int    `json:"hls"`
}

// URLToPlay is the stream the device should be given: the resolved URL the
// catalogue worked out when it checked the station, else the one it was
// given. A resolved URL has its playlists decoded and redirects followed,
// which a Chromecast cannot always do by itself.
func (s Station) URLToPlay() string {
	if s.URLResolved != "" {
		return s.URLResolved
	}
	return s.URL
}

// ContentType is the media type of the stream, worked out from the
// catalogue's codec and whether it is HLS. It is empty when the catalogue
// says nothing useful, which leaves the device to work it out.
func (s Station) ContentType() string {
	if s.HLS != 0 {
		return "application/x-mpegurl"
	}
	switch strings.ToUpper(strings.TrimSpace(s.Codec)) {
	case "MP3", "MP2":
		return "audio/mpeg"
	case "AAC", "AAC+", "AAC+H", "HEAAC":
		return "audio/aac"
	case "OGG", "VORBIS":
		return "audio/ogg"
	case "OPUS":
		return "audio/ogg"
	case "FLAC":
		return "audio/flac"
	}
	return ""
}

// Entry is the station as a favorite: its name cleaned up, its stream
// URL, and its media type when known.
func (s Station) Entry() playlist.Entry {
	return playlist.Entry{
		Name:        CleanName(s.Name),
		URL:         s.URLToPlay(),
		ContentType: s.ContentType(),
	}
}

// CleanName makes a station's name fit a playlist entry: one line, with
// runs of whitespace folded to one space, and no tab, which the playlist
// file separates fields with.
func CleanName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, name)
	return strings.Join(strings.FieldsFunc(name, unicode.IsSpace), " ")
}

// Query asks for a page of stations.
type Query struct {
	// CountryCode limits the search to one country, as "DE". Empty means
	// every country.
	CountryCode string
	// Order sorts the results. Empty means by votes.
	Order Order
	// Reverse reverses the order. The useful orders, votes and clicks,
	// are "most first", so this is true for them.
	Reverse bool
	// Name keeps stations whose name contains it, when not empty.
	Name string
	// Limit is how many stations to ask for. Zero means DefaultLimit.
	Limit int
}

// DefaultLimit is how many stations a query asks for when it names none.
const DefaultLimit = 100

// Error is what the service answered when it refused a request.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("radio-browser answered %d", e.Status)
	}
	return fmt.Sprintf("radio-browser answered %d: %s", e.Status, e.Body)
}

// Options configures a Client.
type Options struct {
	// BaseURL is the service's address. Empty means DefaultBaseURL.
	BaseURL string
	// HTTPClient makes the requests. Nil means one that gives up after
	// 10 seconds.
	HTTPClient *http.Client
	// UserAgent names the client, as the service asks. Empty means
	// "gocc".
	UserAgent string
}

// Client reads the catalogue.
type Client struct {
	baseURL string
	http    *http.Client
	ua      string
}

// New makes a client.
func New(opts Options) *Client {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = "gocc"
	}
	return &Client{baseURL: base, http: hc, ua: ua}
}

// countryJSON is the shape of /json/countries. The service sends the
// station count as a number on some mirrors and a string on others.
type countryJSON struct {
	Name         string          `json:"name"`
	ISO          string          `json:"iso_3166_1"`
	StationCount json.RawMessage `json:"stationcount"`
}

// Countries lists the countries the catalogue has stations in, most
// stations first.
func (c *Client) Countries(ctx context.Context) ([]Country, error) {
	q := url.Values{}
	q.Set("order", "stationcount")
	q.Set("reverse", "true")
	var raw []countryJSON
	if err := c.get(ctx, "/json/countries", q, &raw); err != nil {
		return nil, err
	}
	out := make([]Country, 0, len(raw))
	for _, r := range raw {
		if r.ISO == "" || r.Name == "" {
			continue
		}
		out = append(out, Country{Name: r.Name, Code: r.ISO, Stations: number(r.StationCount)})
	}
	return out, nil
}

// Stations asks for a page of stations.
func (c *Client) Stations(ctx context.Context, query Query) ([]Station, error) {
	q := url.Values{}
	if query.CountryCode != "" {
		q.Set("countrycode", strings.ToUpper(query.CountryCode))
	}
	order := query.Order
	if order == "" {
		order = OrderVotes
	}
	if !order.Valid() {
		return nil, fmt.Errorf("radio-browser: unknown order %q", order)
	}
	q.Set("order", string(order))
	q.Set("reverse", strconv.FormatBool(query.Reverse))
	if name := strings.TrimSpace(query.Name); name != "" {
		q.Set("name", name)
	}
	limit := query.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	q.Set("limit", strconv.Itoa(limit))
	// Broken stations cannot be played, and the catalogue knows which
	// ones failed their last check.
	q.Set("hidebroken", "true")

	var out []Station
	if err := c.get(ctx, "/json/stations/search", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// get reads a JSON document from the service into into.
func (c *Client) get(ctx context.Context, path string, q url.Values, into any) error {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("radio-browser: %w", err)
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("radio-browser: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return &Error{Status: res.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	dec := json.NewDecoder(io.LimitReader(res.Body, 8<<20))
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("radio-browser: reading %s: %w", path, err)
	}
	return nil
}

// number reads a number the service sent as either a number or a string.
func number(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		n, _ = strconv.Atoi(s)
	}
	return n
}

// ErrNoStations is returned when the catalogue has nothing to show.
var ErrNoStations = errors.New("radio-browser: no stations")
