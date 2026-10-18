# gocc

gocc controls Chromecast devices on your local network: it finds them, plays
internet radio on the one you choose, and sets what it plays and how loud.

One core, two front ends with the same features:

| | Binary | Interface | Runs on |
| --- | --- | --- | --- |
| Desktop | `gocc` | A native window drawn by [mygo](https://github.com/egoist/mygo) | A display and GTK 3 |
| Command line | `gocc-cli` | A web page served by the binary | Anything with a browser, including a phone |

Device communication uses
[vishen/go-chromecast](https://github.com/vishen/go-chromecast).

## The interface

Both front ends show the same two rows. The top row holds the devices and
the controls of the chosen one, a third and two thirds of the width; the
bottom row holds **Favorites / Browse** across the whole window.

- **Devices** — what was found on the network; one is controlled at a time.
  A click connects to it, and **Scan** looks again. On a machine with more
  than one network interface (a VPN, a container bridge), the desktop app
  offers a picker; `gocc-cli` takes `--iface` instead.
- **Controls** — the chosen device's state (playing, paused, idle, what it
  plays) and its transport: play/pause, stop, refresh, volume and mute.
- **Favorites** — saved stations and hand-written URLs. Double-click a row
  or press its Play button. Search narrows the list as you type. The form
  at the bottom adds a URL: a name, a URL and Add, on one line.
- **Browse** — online radio from
  [radio-browser.info](https://www.radio-browser.info/). Pick a country
  (the list filters as you type), an order (Votes / Popular / Name), and a
  search word. Stations are square cards: the logo fills the card, the name
  and details float over its bottom, and Play / ♡ appear in the corner on
  hover. Double-click a card, or press Enter on it, to play it.

A device buffers a stream before it makes a sound. Both front ends show a
notice while it does (`⟳ Requesting Living Room…`, then `⟳ Buffering on
Living Room…`), so a request that was heard never looks like one that was
not. If the device never starts, the notice becomes an error after 30
seconds instead of hanging forever.

The catalogue is cached: the country list for 6 hours, each page of
stations for 5 minutes (keyed by country, order and search word), and a
failed read for 30 seconds. Going back to a country, an order or a search
word is instant and makes no request. **Refresh** drops the cache and reads
the page again.

The web page is responsive. Below 720px it becomes one column: devices,
controls, then favorites or browse, with the search field on its own line.

## Desktop app

Needs Go 1.27+ to build, and GTK 3 at run time on Linux:

```sh
go run .                  # run it
go build -o gocc .        # or build it
./gocc
```

## Command line version

No window: it serves the page over HTTP.

```sh
go build -o gocc-cli ./cmd/gocc-cli
./gocc-cli --iface wlan0 --listen 127.0.0.1:8097
```

Then open `http://127.0.0.1:8097`.

### Flags

| Flag | Meaning |
| --- | --- |
| `--listen` | Address and port to serve on, as `127.0.0.1:8097` or `:8097` (default `127.0.0.1:8097`) |
| `--iface` | Network interface to discover devices on, as `wlan0` (default: every interface) |
| `--basic-auth` | Ask browsers for HTTP basic authentication, as `user:password` (default: none) |
| `--data-dir` | Directory holding `favorites.txt` (default: the desktop app's directory, so both share one list) |
| `--version` | Print the version and exit |

Binding to a non-loopback address without `--basic-auth` logs a warning at
startup: anyone who can reach the address can control your devices.

For the local network:

```sh
./gocc-cli --iface wlan0 --listen :8097 --basic-auth alice:s3cret
```

## Building

Go 1.27+. With `make`:

```sh
make              # both binaries into bin/
make gocc         # desktop only
make gocc-cli     # command line only
make install      # into $GOBIN or $GOPATH/bin
make test         # the suite
make test-race    # the suite under the race detector
make dist         # both binaries for every platform, into dist/
make clean        # remove what was built
```

Or with plain `go build`:

```sh
go build -o gocc .                     # desktop
go build -o gocc-cli ./cmd/gocc-cli    # command line
```

`make` bakes the version, commit and build time into the binary with
`-ldflags` (see `internal/version`). The version comes from a git tag, or
from the commit when there is none, as `v1.0.0` or `3dec4af`; a dirty tree
adds `-dirty`. A plain `go build` shows `dev`, which is the honest answer
for a build that does not know its version.

Both binaries answer `--version`:

```sh
$ ./bin/gocc-cli --version
gocc-cli v1.0.1 (a122309, 2026-10-17T18:45:45Z)
```

`gocc --version` returns before the interface is set up, so it works on a
machine with no display.

### Run-time requirements

- **`gocc` (desktop)** needs a display and GTK 3 on Linux. Building it does
  **not** need GTK: the toolkit is called through `purego`, with no cgo, so
  it cross-compiles.
- **`gocc-cli`** needs only a browser. Building and running it need nothing
  else.

`make dist` builds linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 and
windows/amd64, both binaries for each, with the version in each file name.

## Favorites

Both front ends read and write the same file, `favorites.txt`, in the app's
data directory (Linux: `~/.config/gocc/`). It is created on first save, and
may be edited by hand.

It is a plain text file, one entry per line, fields separated by tabs:

```
# gocc favorites: name<TAB>url[<TAB>content type]
radio1	http://radio1.com/1.mp3
jazz	http://jazz.example/stream	audio/aac
```

Blank lines and lines starting with `#` are ignored. The third field
(content type) is optional; it is inferred from the URL's extension, and
only when that says nothing is a HEAD request made, with a timeout, to ask
the server. A URL already saved is not saved again.

## Code structure

| Directory | Purpose |
| --- | --- |
| `main.go` | Desktop entry: creates the window and mounts the interface |
| `cmd/gocc-cli/` | Command line entry: flags, HTTP server |
| `core/` | **The core**: devices, connection, state, favorites, catalogue and its cache. No interface code. Both front ends build on it |
| `app/` | Desktop interface: window, control state, the two rows |
| `web/` | Command line interface: HTTP routes, server-sent events, the embedded page (`web/ui/index.html`) |
| `cast/` | Chromecast: discovery (`discovery.go`), one device (`client.go`), content type inference (`contenttype.go`), volume throttling (`volume.go`) |
| `radiobrowser/` | radio-browser.info client: countries, station search, content type mapping |
| `playlist/` | Reading and writing the favorites text file |
| `internal/version/` | The version baked in at build time |
| `Makefile` | Builds both binaries |

The boundary is the `core` package: it knows nothing of windows, frames or
the DOM, and `app` and `web` each turn an `Engine.Snapshot()` into their own
interface. A new front end does not touch the other, and tests can drive the
core without an interface at all.

`core.Options` takes injectable `NewController`, `Discover` and `Stations`
functions, so tests can supply a fake device, a fixed discovery and a fixed
catalogue without a network.

### Design notes

**Device commands are serialized.** The upstream library is not safe for
concurrent use (its own source says `TODO: nothing here is concurrent
safe`). `cast.Client` queues every command and runs them in order on one
goroutine; the interface only enqueues and never waits. `Close` waits for
that goroutine to stop before closing the connection.

**Volume commands are throttled.** A device plays its own sound for every
volume command, and a drag of the slider asks for a new level on every
frame. So volume is coalesced in `cast.Client`: at most one command every
200ms, with the latest level replacing any waiting one, and the final level
always sent. The first change after a quiet spell goes out at once, so a
single click is not delayed. Mute is a single action and is not throttled.

**State is pushed to both front ends.** `core` calls a change callback:
- the desktop app connects it to `Window.Invalidate`, so the window redraws,
  and the view reads a consistent snapshot;
- `gocc-cli` connects it to a broadcast hub, and browsers subscribe with
  `GET /api/events` (server-sent events). The page updates the DOM in place
  rather than rebuilding it, so a state push never resets a dragged volume
  slider or a scrolled list. A browser test guards this.

**The catalogue is cached in the core.** Pages are keyed by country, order,
search word and limit; concurrent requests for the same page share one
network call; going back to a cached page is synchronous and shows no
spinner.

**Logos are loaded lazily, by URL.** Many stations share one picture, so it
is downloaded once and shown in as many cards as need it. A logo that fails
is left alone for 5 minutes. The cache is bounded (96 bitmaps);
the least recently used is dropped. The download runs off the main thread;
the bitmap is stored without touching the window, so a closed window does
not leak a load counter.

## Tests

```sh
go test ./...              # everything
go test -race ./...        # with the race detector
```

- `core/`, `playlist/`, `radiobrowser/` — pure logic, driven by a fake device,
  fake discovery and a fake catalogue; no network. `radiobrowser` uses
  `httptest` with fixed JSON.
- `app/` — the desktop view is driven by mygo's `ui.Tester` without a window:
  clicks, double-clicks, slider drags and typing, asserting the commands sent
  to the device.
- `web/` — the HTTP layer is tested with `httptest` (basic auth, server-sent
  events). `web/browser_test.go` goes further: it drives a **real page in a
  headless browser** through chromedriver (and geckodriver for Firefox),
  checking that favorites and the catalogue browse and play, that station
  cards are square with their buttons over the picture, that a state push
  does not disturb a dragged slider or a scrolled list, that the two rows
  become one column on a phone, and that grid rows are spaced correctly in
  both browsers. These tests skip themselves when the drivers are missing.

To see the desktop interface without a display:

```sh
GOCC_SNAPSHOT=1 go test ./app/ -run TestRenderSnapshot
# writes /tmp/gocc-snapshot-favorites.png and /tmp/gocc-snapshot-browse.png
```

The command line version's page is just its URL in a browser.
