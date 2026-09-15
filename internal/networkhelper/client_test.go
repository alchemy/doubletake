package networkhelper

import (
	"bufio"
	"context"
	"net"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

type testBroker struct {
	mu       sync.Mutex
	alive    bool
	ended    bool
	received int
}

func (b *testBroker) Begin(sender dbus.Sender, control, timing, audioControl, audioData dbus.UnixFD) *dbus.Error {
	udp := []dbus.UnixFD{timing, audioControl, audioData}
	defer unix.Close(int(control))
	for _, fd := range udp {
		defer unix.Close(int(fd))
	}
	if _, err := unix.Getpeername(int(control)); err != nil {
		return dbus.MakeFailedError(err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.received = len(udp)
	b.alive = true
	return nil
}
func (b *testBroker) Alive(sender dbus.Sender) (bool, *dbus.Error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.alive, nil
}
func (b *testBroker) End(sender dbus.Sender) *dbus.Error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.alive = false
	b.ended = true
	return nil
}

func TestDescriptorPassingAndLeaseLoss(t *testing.T) {
	if _, e := exec.LookPath("dbus-daemon"); e != nil {
		t.Skip("dbus-daemon unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "dbus-daemon", "--session", "--nofork", "--print-address=1")
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); cmd.Wait() }()
	scanner := bufio.NewScanner(pipe)
	if !scanner.Scan() {
		t.Fatal("no private bus address")
	}
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", scanner.Text())
	bus, e := dbus.ConnectSystemBus()
	if e != nil {
		t.Fatal(e)
	}
	defer bus.Close()
	broker := &testBroker{}
	if e = bus.Export(broker, Path, BusName); e != nil {
		t.Fatal(e)
	}
	if _, e = bus.RequestName(BusName, dbus.NameFlagDoNotQueue); e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	conn, e := net.Dial("tcp4", listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	peer, e := listener.Accept()
	if e != nil {
		t.Fatal(e)
	}
	defer peer.Close()
	var ports []net.PacketConn
	for i := 0; i < 3; i++ {
		p, e := net.ListenPacket("udp4", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		defer p.Close()
		ports = append(ports, p)
	}
	session, e := Begin(ctx, conn, ports)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	broker.mu.Lock()
	n := broker.received
	broker.alive = false
	broker.mu.Unlock()
	if n != 3 {
		t.Fatalf("received %d descriptors", n)
	}
	lost := make(chan struct{})
	go session.Watch(ctx, func() { close(lost) })
	select {
	case <-lost:
	case <-time.After(5 * time.Second):
		t.Fatal("lease loss not propagated")
	}
	session.Close()
	session.Close()
	broker.mu.Lock()
	ended := broker.ended
	broker.mu.Unlock()
	if !ended {
		t.Fatal("End not delivered")
	}
	// Passing duplicate descriptors must leave the original application sockets usable.
	if _, e = ports[0].WriteTo([]byte("x"), ports[1].LocalAddr()); e != nil {
		t.Fatal(e)
	}
}
