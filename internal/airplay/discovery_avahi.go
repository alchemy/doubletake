package airplay

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const avahiService = "org.freedesktop.Avahi"
const avahiBrowser = avahiService + ".ServiceBrowser"

type avahiKey struct {
	Interface int32
	Protocol  int32
	Name      string
	Type      string
	Domain    string
}

type avahiResolution struct {
	key        avahiKey
	generation uint64
	device     AirPlayDevice
	err        error
}

// One browser per epoch; periodic epochs refresh cached TXT/address records and
// allow recovery after Avahi restarts. Resolution runs independently of signals
// so a missing host cannot hold up other receivers or removal notifications.
func browseAvahi(ctx context.Context, emit func(DiscoveryEvent)) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	return browseAvahiConn(ctx, conn, emit)
}

func browseAvahiConn(ctx context.Context, conn *dbus.Conn, emit func(DiscoveryEvent)) error {
	signals := make(chan *dbus.Signal, 256)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	if err := conn.AddMatchSignal(dbus.WithMatchSender(avahiService), dbus.WithMatchInterface(avahiBrowser)); err != nil {
		return err
	}
	setup, cancel := context.WithTimeout(ctx, time.Second)
	var path dbus.ObjectPath
	err := conn.Object(avahiService, "/").CallWithContext(setup, avahiService+".Server.ServiceBrowserNew", 0,
		int32(-1), int32(-1), "_airplay._tcp", "local", uint32(0)).Store(&path)
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn.Object(avahiService, path).CallWithContext(cleanup, avahiBrowser+".Free", 0)
	}()
	dbg("[DISCOVERY] browsing through Avahi")
	resolveCtx, stop := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { stop(); workers.Wait() }()
	results := make(chan avahiResolution, 256)
	active := 0
	var pending []avahiResolution
	generations := make(map[avahiKey]uint64)
	resolved := make(map[avahiKey]AirPlayDevice)
	var sequence uint64
	remove := func(key avahiKey) {
		dev, ok := resolved[key]
		delete(resolved, key)
		if !ok {
			return
		}
		for _, other := range resolved {
			if other.IP == dev.IP {
				return
			}
		}
		emit(DiscoveryEvent{Device: dev, Removed: true})
	}
	for {
		for active < 8 && len(pending) > 0 {
			request := pending[0]
			pending = pending[1:]
			if generations[request.key] != request.generation {
				continue
			}
			active++
			workers.Add(1)
			go func(request avahiResolution) {
				defer workers.Done()
				request.device, request.err = resolveAvahi(resolveCtx, conn, request.key)
				select {
				case results <- request:
				case <-resolveCtx.Done():
				}
			}(request)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-conn.Context().Done():
			return fmt.Errorf("Avahi bus disconnected")
		case result := <-results:
			active--
			if generations[result.key] != result.generation {
				continue
			}
			if result.err != nil {
				delete(generations, result.key)
				continue
			}
			if old, ok := resolved[result.key]; ok && old.IP != result.device.IP {
				remove(result.key)
			}
			resolved[result.key] = result.device
			emit(DiscoveryEvent{Device: result.device})
		case signal := <-signals:
			if signal == nil {
				return fmt.Errorf("Avahi signal stream closed")
			}
			if signal.Path != path {
				continue
			}
			if signal.Name == avahiBrowser+".Failure" {
				return fmt.Errorf("Avahi browser failed: %v", signal.Body)
			}
			if signal.Name != avahiBrowser+".ItemNew" && signal.Name != avahiBrowser+".ItemRemove" {
				continue
			}
			var key avahiKey
			var flags uint32
			if err := dbus.Store(signal.Body, &key.Interface, &key.Protocol, &key.Name, &key.Type, &key.Domain, &flags); err != nil {
				continue
			}
			if signal.Name == avahiBrowser+".ItemRemove" {
				delete(generations, key)
				remove(key)
				continue
			}
			if _, exists := generations[key]; exists {
				continue
			}
			sequence++
			generations[key] = sequence
			pending = append(pending, avahiResolution{key: key, generation: sequence})
		}
	}
}

func resolveAvahi(ctx context.Context, conn *dbus.Conn, key avahiKey) (AirPlayDevice, error) {
	// Prefer an IPv4 connection when available, just like the native backend.
	var lastErr error
	for _, family := range []int32{0, -1} {
		query, cancel := context.WithTimeout(ctx, time.Second)
		var iface, protocol, addressProtocol int32
		var name, service, domain, host, address string
		var port uint16
		var txt [][]byte
		var flags uint32
		err := conn.Object(avahiService, "/").CallWithContext(query, avahiService+".Server.ResolveService", 0,
			key.Interface, key.Protocol, key.Name, key.Type, key.Domain, family, uint32(0)).Store(
			&iface, &protocol, &name, &service, &domain, &host, &addressProtocol, &address, &port, &txt, &flags)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		if !isUsableMDNSAddress(net.ParseIP(address)) || port == 0 {
			lastErr = fmt.Errorf("unusable Avahi address")
			continue
		}
		dev := AirPlayDevice{Name: name, IP: address, Port: int(port)}
		records := make([]string, len(txt))
		for i, value := range txt {
			records[i] = string(value)
		}
		populateDeviceFromTXT(&dev, parseTXT(records))
		return dev, nil
	}
	return AirPlayDevice{}, lastErr
}
