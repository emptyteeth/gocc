package core

import (
	"errors"
	"testing"
	"time"

	"github.com/emptyteeth/gocc/radiobrowser"
)

// clock is a time the test moves.
type clock struct{ at time.Time }

func (c *clock) now() time.Time          { return c.at }
func (c *clock) advance(d time.Duration) { c.at = c.at.Add(d) }

func newClock() *clock { return &clock{at: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)} }

func testKey() stationKey {
	return stationKey{country: "DE", order: radiobrowser.OrderVotes, query: "", limit: 100}
}

func TestStationCacheHitsAndExpires(t *testing.T) {
	c := newStationCache()
	clk := newClock()
	c.now = clk.now

	stations := []radiobrowser.Station{{UUID: "a", Name: "A"}}
	c.put(testKey(), stations, nil)

	got, err, ok := c.get(testKey())
	if !ok || err != nil {
		t.Fatalf("the page was not cached: ok=%v err=%v", ok, err)
	}
	if len(got) != 1 || got[0].Name != "A" {
		t.Fatalf("got %+v", got)
	}

	// A different question is not the same page.
	if _, _, ok := c.get(stationKey{country: "FR", order: radiobrowser.OrderVotes, limit: 100}); ok {
		t.Error("another country hit the cache")
	}
	if _, _, ok := c.get(stationKey{country: "DE", order: radiobrowser.OrderBitrate, limit: 100}); ok {
		t.Error("another order hit the cache")
	}
	if _, _, ok := c.get(stationKey{country: "DE", order: radiobrowser.OrderVotes, query: "jazz", limit: 100}); ok {
		t.Error("a search hit the cache of no search")
	}

	clk.advance(stationsTTL - time.Second)
	if _, _, ok := c.get(testKey()); !ok {
		t.Error("the page expired too early")
	}
	clk.advance(2 * time.Second)
	if _, _, ok := c.get(testKey()); ok {
		t.Error("the page outlived its time")
	}
}

func TestStationCacheCopies(t *testing.T) {
	c := newStationCache()
	stations := []radiobrowser.Station{{UUID: "a", Name: "A"}}
	c.put(testKey(), stations, nil)

	stations[0].Name = "changed by the caller"
	got, _, ok := c.get(testKey())
	if !ok || got[0].Name != "A" {
		t.Fatalf("the cache kept the caller's slice: %+v", got)
	}

	got[0].Name = "changed by the reader"
	again, _, _ := c.get(testKey())
	if again[0].Name != "A" {
		t.Fatalf("the cache handed its own slice out: %+v", again)
	}
}

func TestStationCacheRemembersFailuresBriefly(t *testing.T) {
	c := newStationCache()
	clk := newClock()
	c.now = clk.now

	failure := errors.New("the network is down")
	c.put(testKey(), nil, failure)

	_, err, ok := c.get(testKey())
	if !ok || !errors.Is(err, failure) {
		t.Fatalf("the failure was not cached: ok=%v err=%v", ok, err)
	}

	// A failure is forgotten sooner than a page.
	clk.advance(errorTTL + time.Second)
	if _, _, ok := c.get(testKey()); ok {
		t.Error("the failure outlived its short time")
	}
}

func TestStationCacheDropsTheOldest(t *testing.T) {
	c := newStationCache()
	c.max = 2
	c.put(stationKey{country: "DE"}, nil, nil)
	c.put(stationKey{country: "FR"}, nil, nil)
	c.put(stationKey{country: "US"}, nil, nil)

	if _, _, ok := c.get(stationKey{country: "DE"}); ok {
		t.Error("the oldest page was not dropped")
	}
	if _, _, ok := c.get(stationKey{country: "FR"}); !ok {
		t.Error("a newer page was dropped")
	}
	if _, _, ok := c.get(stationKey{country: "US"}); !ok {
		t.Error("the newest page was dropped")
	}
}

func TestStationCacheUseKeepsAPage(t *testing.T) {
	c := newStationCache()
	c.max = 2
	c.put(stationKey{country: "DE"}, nil, nil)
	c.put(stationKey{country: "FR"}, nil, nil)

	// Using DE makes FR the least recently used.
	if _, _, ok := c.get(stationKey{country: "DE"}); !ok {
		t.Fatal("DE is gone")
	}
	c.put(stationKey{country: "US"}, nil, nil)

	if _, _, ok := c.get(stationKey{country: "DE"}); !ok {
		t.Error("the page just used was dropped")
	}
	if _, _, ok := c.get(stationKey{country: "FR"}); ok {
		t.Error("the least recently used page was kept")
	}
}

func TestCountryCache(t *testing.T) {
	c := newCountryCache()
	clk := newClock()
	c.now = clk.now

	if _, ok := c.get(); ok {
		t.Error("an empty cache hit")
	}
	c.put([]radiobrowser.Country{{Name: "Germany", Code: "DE"}})
	got, ok := c.get()
	if !ok || len(got) != 1 || got[0].Code != "DE" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}

	clk.advance(countriesTTL - time.Second)
	if _, ok := c.get(); !ok {
		t.Error("the countries expired too early")
	}
	clk.advance(2 * time.Second)
	if _, ok := c.get(); ok {
		t.Error("the countries outlived their time")
	}
}

func TestCountryCacheClear(t *testing.T) {
	c := newCountryCache()
	c.put([]radiobrowser.Country{{Code: "DE"}})
	c.clear()
	if _, ok := c.get(); ok {
		t.Error("the countries survived a clear")
	}
}

func TestStationCachePutsTheSamePageTwiceCleanly(t *testing.T) {
	c := newStationCache()
	c.max = 2
	c.put(testKey(), nil, nil)
	c.put(testKey(), nil, nil) // the same page again
	c.put(stationKey{country: "FR"}, nil, nil)

	// A page put again is one entry in the order, not two: the order must
	// not hold a key twice, or eviction could drop a page still in use.
	if len(c.order) != len(c.pages) {
		t.Fatalf("order holds %d entries for %d pages", len(c.order), len(c.pages))
	}

	// The page put once is the oldest used, so it is the one dropped when
	// another arrives; the one put twice survives.
	c.put(stationKey{country: "US"}, nil, nil)
	if _, _, ok := c.get(testKey()); ok {
		t.Error("the least recently used page was kept")
	}
	if _, _, ok := c.get(stationKey{country: "FR"}); !ok {
		t.Error("a newer page was dropped")
	}
}
