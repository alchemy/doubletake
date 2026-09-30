package daemon

import (
	"doubletake/internal/airplay"
	"testing"
	"time"
)

func TestDiscoveryPublishesBeforeScanEndsAndExpires(t *testing.T) {
	d := &Daemon{discoverRefresh: make(chan struct{}, 1)}
	now := time.Now()
	event := airplay.DiscoveryEvent{Device: airplay.AirPlayDevice{Name: "Apple TV", IP: "192.0.2.10", Port: 7000}}
	d.updateDiscoveredDevice(event, now)
	if got := d.handleDiscover(); len(got.Devices) != 1 {
		t.Fatal("resolved receiver not immediately visible")
	}
	for i := 0; i < 20; i++ {
		d.handleDiscover()
	}
	if len(d.discoverRefresh) != 1 {
		t.Fatal("refresh requests did not coalesce")
	}
	event.Device.Name = "Updated TV"
	d.updateDiscoveredDevice(event, now.Add(time.Second))
	if len(d.devices) != 1 || d.devices[0].Name != "Updated TV" {
		t.Fatal("duplicate was not updated")
	}
	d.expireDiscoveredDevices(now.Add(30 * time.Second))
	if len(d.devices) != 1 {
		t.Fatal("fresh device expired")
	}
	d.expireDiscoveredDevices(now.Add(32 * time.Second))
	if len(d.devices) != 0 || len(d.deviceLastSeen) != 0 {
		t.Fatal("stale device retained")
	}
	d.updateDiscoveredDevice(event, now)
	event.Removed = true
	d.updateDiscoveredDevice(event, now)
	if len(d.handleDevices().Devices) != 0 {
		t.Fatal("removed receiver retained")
	}
}
