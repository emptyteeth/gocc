package core

import (
	"sync"
	"time"

	"github.com/free/gocc/radiobrowser"
)

// The catalogue is read over the network, and each answer takes a couple
// of seconds: the service has no cache headers of its own, so a client
// that asks again for what it just read pays for it every time. These
// caches keep the answers around for a while, which makes going back to a
// country, an order or a search word instant.
const (
	// stationsTTL is how long a page of stations is trusted. Votes and
	// clicks move continuously, but not so fast that a few minutes of
	// staleness shows; stream URLs change far more rarely.
	stationsTTL = 5 * time.Minute
	// countriesTTL is how long the country list is trusted. Its station
	// counts drift, but the list of countries itself is stable.
	countriesTTL = 6 * time.Hour
	// errorTTL is how long a failed read is remembered, so that a service
	// that is down is not asked again on every keystroke.
	errorTTL = 30 * time.Second
	// maxCachedStations is the most queries kept. A page is a hundred
	// stations, so this bounds the cache at a few megabytes.
	maxCachedStations = 64
)

// stationKey names one page of stations: a page is only the same as
// another when every part of the question is.
type stationKey struct {
	country string
	order   radiobrowser.Order
	query   string
	limit   int
}

// stationPage is a cached answer, or the failure that stood in for one.
type stationPage struct {
	stations []radiobrowser.Station
	err      error
	at       time.Time
}

// stationCache remembers the pages read, newest last in order so that the
// least recently used is the one dropped when it is full.
type stationCache struct {
	mu          sync.Mutex
	pages       map[stationKey]stationPage
	order       []stationKey
	max         int
	now         func() time.Time
	stationsTTL time.Duration
	errorTTL    time.Duration
}

func newStationCache() *stationCache {
	return &stationCache{
		pages:       map[stationKey]stationPage{},
		max:         maxCachedStations,
		now:         time.Now,
		stationsTTL: stationsTTL,
		errorTTL:    errorTTL,
	}
}

// get returns the page cached for key, if it is still fresh, with the
// error the read made, if it failed. A page read from the cache is a copy,
// so that a caller changing it cannot change what the next caller sees.
func (c *stationCache) get(key stationKey) ([]radiobrowser.Station, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	page, ok := c.pages[key]
	if !ok {
		return nil, nil, false
	}
	ttl := c.stationsTTL
	if page.err != nil {
		ttl = c.errorTTL
	}
	if c.now().Sub(page.at) > ttl {
		c.dropLocked(key)
		return nil, nil, false
	}
	c.touchLocked(key)
	if page.err != nil {
		return nil, page.err, true
	}
	return append([]radiobrowser.Station(nil), page.stations...), nil, true
}

// put remembers a page, or the failure that stood in for one.
func (c *stationCache) put(key stationKey, stations []radiobrowser.Station, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.pages[key]; ok {
		c.touchLocked(key)
	}
	c.pages[key] = stationPage{
		stations: append([]radiobrowser.Station(nil), stations...),
		err:      err,
		at:       c.now(),
	}
	c.order = append(c.order, key)
	for len(c.pages) > c.max && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		if _, ok := c.pages[oldest]; ok {
			delete(c.pages, oldest)
		}
	}
}

// clear forgets everything, as a scan asks: the catalogue may have
// changed everywhere, not just in one page.
func (c *stationCache) clear() {
	c.mu.Lock()
	c.pages = map[stationKey]stationPage{}
	c.order = nil
	c.mu.Unlock()
}

// dropLocked removes key. The caller holds mu.
func (c *stationCache) dropLocked(key stationKey) {
	delete(c.pages, key)
	c.removeOrderLocked(key)
}

// touchLocked makes key the newest one used. The caller holds mu.
func (c *stationCache) touchLocked(key stationKey) {
	c.removeOrderLocked(key)
	c.order = append(c.order, key)
}

// removeOrderLocked takes key out of the order. The caller holds mu.
func (c *stationCache) removeOrderLocked(key stationKey) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// countryCache remembers the country list.
type countryCache struct {
	mu   sync.Mutex
	list []radiobrowser.Country
	at   time.Time
	ttl  time.Duration
	now  func() time.Time
}

func newCountryCache() *countryCache {
	return &countryCache{ttl: countriesTTL, now: time.Now}
}

// get returns the countries, if they are still fresh.
func (c *countryCache) get() ([]radiobrowser.Country, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.list == nil || c.now().Sub(c.at) > c.ttl {
		return nil, false
	}
	return append([]radiobrowser.Country(nil), c.list...), true
}

// put remembers the countries.
func (c *countryCache) put(countries []radiobrowser.Country) {
	c.mu.Lock()
	c.list = append([]radiobrowser.Country(nil), countries...)
	c.at = c.now()
	c.mu.Unlock()
}

// clear forgets the countries.
func (c *countryCache) clear() {
	c.mu.Lock()
	c.list = nil
	c.at = time.Time{}
	c.mu.Unlock()
}
