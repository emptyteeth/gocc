// Package cast discovers Chromecast devices on the local network and
// controls one of them at a time: what it plays, whether it is paused, and
// how loud it is.
//
// The upstream library is not safe to use from several goroutines at once,
// so a Client runs every command on one goroutine of its own, in the order
// they arrive, and never touches the device from anywhere else.
package cast

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	castdns "github.com/vishen/go-chromecast/dns"
)

// Device is a Chromecast found on the network.
type Device struct {
	// UUID identifies the device across discoveries and reconnects.
	UUID string `json:"uuid"`
	// Name is what the device calls itself, as "Living Room".
	Name string `json:"name"`
	// Model is the device's model name, as "Chromecast Ultra".
	Model string `json:"model"`
	// Addr and Port are where the device answers.
	Addr string `json:"addr"`
	Port int    `json:"port"`
	// iface is the network interface the device was found on.
	iface *net.Interface
}

// String names the device for a log line.
func (d Device) String() string {
	if d.Name != "" {
		return fmt.Sprintf("%s (%s)", d.Name, d.Addr)
	}
	return d.Addr
}

// Discovery finds Chromecast devices on the network and reports the ones
// that appear, and the ones that go away, as they change.
type Discovery struct {
	// Iface limits discovery to one network interface. A machine with
	// several interfaces, as a VPN or a container bridge beside its
	// network card, needs it: mDNS answers on all of them, and the
	// devices behind a VPN are usually not the ones wanted. Nil means
	// every interface.
	Iface *net.Interface

	// OnDevices is called whenever the set of devices changes, with the
	// devices found so far, in the order they were first seen. It is
	// called from the discovery goroutine.
	OnDevices func([]Device)

	mu      sync.Mutex
	devices []Device
	seen    map[string]int
	cancel  context.CancelFunc
	done    chan struct{}
}

// Start begins discovering devices, and keeps discovering them until Stop.
// Devices that answer more than once, as mDNS repeats, are reported once.
func (d *Discovery) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.cancel != nil {
		d.mu.Unlock()
		return errors.New("cast: discovery is already running")
	}
	if d.seen == nil {
		d.seen = map[string]int{}
	}
	ctx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.done = make(chan struct{})
	done := d.done
	d.mu.Unlock()

	entries, err := castdns.DiscoverCastDNSEntries(ctx, d.Iface)
	if err != nil {
		cancel()
		d.mu.Lock()
		d.cancel, d.done = nil, nil
		d.mu.Unlock()
		return fmt.Errorf("cast: discovery: %w", err)
	}

	go func() {
		defer close(done)
		for entry := range entries {
			device := Device{
				UUID:  entry.GetUUID(),
				Name:  entry.GetName(),
				Model: entry.Device,
				Addr:  entry.GetAddr(),
				Port:  entry.GetPort(),
				iface: d.Iface,
			}
			if device.UUID == "" || device.Addr == "" {
				continue
			}
			d.add(device)
		}
	}()
	return nil
}

// Stop ends discovery.
func (d *Discovery) Stop() {
	d.mu.Lock()
	cancel, done := d.cancel, d.done
	d.cancel, d.done = nil, nil
	d.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

// Devices returns the devices found so far.
func (d *Discovery) Devices() []Device {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Device(nil), d.devices...)
}

// Find returns the device with this UUID, and whether it is known.
func (d *Discovery) Find(uuid string) (Device, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, dev := range d.devices {
		if dev.UUID == uuid {
			return dev, true
		}
	}
	return Device{}, false
}

// add records a device the first time it answers, and tells the app when
// that changes the set.
func (d *Discovery) add(device Device) {
	d.mu.Lock()
	if _, ok := d.seen[device.UUID]; ok {
		// The device is known. Keep the name fresh: some devices only
		// carry theirs in later answers.
		if i := d.seen[device.UUID]; d.devices[i].Name == "" && device.Name != "" {
			d.devices[i].Name = device.Name
		}
		d.mu.Unlock()
		return
	}
	d.seen[device.UUID] = len(d.devices)
	d.devices = append(d.devices, device)
	devices := append([]Device(nil), d.devices...)
	d.mu.Unlock()

	if d.OnDevices != nil {
		d.OnDevices(devices)
	}
}

// Interfaces lists the network interfaces a device could be found on:
// those that are up, are not loopback, and carry an address. An app that
// finds no device offers them to choose from.
func Interfaces() ([]net.Interface, error) {
	all, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []net.Interface
	for _, iface := range all {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}
		if !hasRoutableAddr(addrs) {
			continue
		}
		out = append(out, iface)
	}
	return out, nil
}

// hasRoutableAddr reports whether the interface carries an address that a
// Chromecast could be reached at, rather than only a link-local one.
func hasRoutableAddr(addrs []net.Addr) bool {
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			continue
		}
		return true
	}
	return false
}

// InterfaceByName returns the interface called name, as "wlan0".
func InterfaceByName(name string) (*net.Interface, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil
	}
	return net.InterfaceByName(name)
}
