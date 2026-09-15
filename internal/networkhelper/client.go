// Package networkhelper is the unprivileged client of the optional host helper.
package networkhelper

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const BusName = "org.doubletake.Network1"
const Path dbus.ObjectPath = "/org/doubletake/Network1"
const Action = "org.doubletake.network.configure"

type Session struct {
	bus  *dbus.Conn
	once sync.Once
}

// Begin uses a dedicated bus connection, so caller loss also ends this lease.
func Begin(ctx context.Context, control net.Conn, ports []net.PacketConn) (*Session, error) {
	if len(ports) != 3 {
		return nil, fmt.Errorf("expected three UDP sockets")
	}
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			bus.Close()
		}
	}()
	var files []*os.File
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	take := func(v any) (dbus.UnixFD, error) {
		s, ok := v.(interface{ File() (*os.File, error) })
		if !ok {
			return 0, fmt.Errorf("unsupported socket type %T", v)
		}
		f, e := s.File()
		if e != nil {
			return 0, e
		}
		files = append(files, f)
		return dbus.UnixFD(f.Fd()), nil
	}
	fd, err := take(control)
	if err != nil {
		return nil, err
	}
	var udp []dbus.UnixFD
	for _, p := range ports {
		f, e := take(p)
		if e != nil {
			return nil, e
		}
		udp = append(udp, f)
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = bus.Object(BusName, Path).CallWithContext(callCtx, BusName+".Begin", 0, fd, udp[0], udp[1], udp[2]).Err
	if err != nil {
		return nil, fmt.Errorf("network helper: %w (install contrib/omarchy/networkd or use -network-helper=false with manually configured networking)", err)
	}
	ok = true
	return &Session{bus: bus}, nil
}

// Watch stops the stream when the helper loses its lease (including reloads).
func (s *Session) Watch(ctx context.Context, lost func()) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c, cancel := context.WithTimeout(ctx, 3*time.Second)
			var alive bool
			err := s.bus.Object(BusName, Path).CallWithContext(c, BusName+".Alive", 0).Store(&alive)
			cancel()
			if err != nil || !alive {
				if ctx.Err() == nil {
					lost()
				}
				return
			}
		}
	}
}
func (s *Session) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.bus.Object(BusName, Path).CallWithContext(ctx, BusName+".End", 0)
		s.bus.Close()
	})
}
