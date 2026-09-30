package airplay

import (
	"bufio"
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

const testBrowserPath dbus.ObjectPath = "/test/browser"

type testAvahiServer struct{ ready chan struct{} }

func (s *testAvahiServer) ServiceBrowserNew(int32, int32, string, string, uint32) (dbus.ObjectPath, *dbus.Error) {
	close(s.ready)
	return testBrowserPath, nil
}
func (s *testAvahiServer) ResolveService(iface, proto int32, name, service, domain string, family int32, flags uint32) (int32, int32, string, string, string, string, int32, string, uint16, [][]byte, uint32, *dbus.Error) {
	if name == "slow" {
		time.Sleep(200 * time.Millisecond)
	}
	return iface, proto, name, service, domain, "receiver.local", 0, "192.0.2.10", 7000, [][]byte{[]byte("model=AppleTV5,3"), []byte("deviceid=test-id")}, 0, nil
}

type testAvahiBrowser struct{}

func (*testAvahiBrowser) Free() *dbus.Error { return nil }

func privateAvahi(t *testing.T) (*dbus.Conn, *dbus.Conn, *testAvahiServer) {
	t.Helper()
	binary, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed")
	}
	cmd := exec.Command(binary, "--session", "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	reader := bufio.NewScanner(stdout)
	if !reader.Scan() {
		t.Fatal("private bus did not print address")
	}
	server, err := dbus.Connect(reader.Text())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	client, err := dbus.Connect(reader.Text())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	if _, err = server.RequestName(avahiService, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	mock := &testAvahiServer{ready: make(chan struct{})}
	if err = server.Export(mock, "/", avahiService+".Server"); err != nil {
		t.Fatal(err)
	}
	if err = server.Export(&testAvahiBrowser{}, testBrowserPath, avahiBrowser); err != nil {
		t.Fatal(err)
	}
	return server, client, mock
}

func TestAvahiIncrementalResolutionAndRemoval(t *testing.T) {
	server, client, mock := privateAvahi(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan DiscoveryEvent, 16)
	done := make(chan error, 1)
	go func() { done <- browseAvahiConn(ctx, client, func(e DiscoveryEvent) { events <- e }) }()
	select {
	case <-mock.ready:
	case <-time.After(time.Second):
		t.Fatal("browser did not start")
	}
	signal := func(kind, name string, iface int32) {
		t.Helper()
		if err := server.Emit(testBrowserPath, avahiBrowser+"."+kind, iface, int32(0), name, "_airplay._tcp", "local", uint32(0)); err != nil {
			t.Fatal(err)
		}
	}
	next := func() DiscoveryEvent {
		t.Helper()
		select {
		case e := <-events:
			return e
		case <-time.After(time.Second):
			t.Fatal("missing incremental event")
			return DiscoveryEvent{}
		}
	}
	signal("ItemNew", "slow", 1)
	signal("ItemNew", "Apple TV", 1)
	e := next()
	if e.Device.Name != "Apple TV" || e.Device.Model != "AppleTV5,3" || e.Removed {
		t.Fatalf("slow resolution blocked fast receiver: %+v", e)
	}
	// Removing an unresolved service must invalidate its eventual reply.
	signal("ItemRemove", "slow", 1)
	signal("ItemNew", "Apple TV", 2)
	_ = next()
	signal("ItemRemove", "Apple TV", 1)
	select {
	case e := <-events:
		t.Fatalf("removed device with a live second advertisement: %+v", e)
	case <-time.After(250 * time.Millisecond):
	}
	signal("ItemRemove", "Apple TV", 2)
	if e = next(); !e.Removed || e.Device.IP != "192.0.2.10" {
		t.Fatalf("missing final removal: %+v", e)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop browser")
	}
}

func TestAvahiBrowserFailure(t *testing.T) {
	server, client, mock := privateAvahi(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- browseAvahiConn(ctx, client, func(DiscoveryEvent) {}) }()
	select {
	case <-mock.ready:
	case <-time.After(time.Second):
		t.Fatal("browser did not start")
	}
	if err := server.Emit(testBrowserPath, avahiBrowser+".Failure", "disconnected"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("failure must trigger fallback")
		}
	case <-time.After(time.Second):
		t.Fatal("browser failure ignored")
	}
}

func TestDiscoveryFallbackAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if canceled {
			cancel()
		}
		fallbackCalled := false
		emitted := false
		err := browseWithFallback(ctx, func(DiscoveryEvent) { emitted = true },
			func(context.Context, func(DiscoveryEvent)) error { return context.DeadlineExceeded },
			func(ctx context.Context, emit func(DiscoveryEvent)) error {
				fallbackCalled = true
				emit(DiscoveryEvent{Device: AirPlayDevice{IP: "192.0.2.1"}})
				cancel()
				return nil
			})
		cancel()
		if err != nil || fallbackCalled == canceled || emitted == canceled {
			t.Fatalf("canceled=%v, fallback=%v, emitted=%v, err=%v", canceled, fallbackCalled, emitted, err)
		}
	}
}
