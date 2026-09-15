package networkd

import (
	"bufio"
	"context"
	"doubletake/internal/networkhelper"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

type fakeFirewall struct {
	rules  []string
	intact bool
	err    error
}

func (f *fakeFirewall) Apply(r []string) error { f.rules = append([]string{}, r...); return f.err }
func (f *fakeFirewall) Intact() bool           { return f.intact }
func TestIndependentSessions(t *testing.T) {
	fw := &fakeFirewall{intact: true}
	s := New(nil, fw)
	s.sessions[":1.1"] = &lease{rules: []string{"first"}}
	s.sessions[":1.2"] = &lease{rules: []string{"second"}}
	if e := s.End(":1.1"); e != nil {
		t.Fatal(e)
	}
	if len(fw.rules) != 1 || fw.rules[0] != "second" {
		t.Fatal(fw.rules)
	}
	if alive, _ := s.Alive(":1.2"); !alive {
		t.Fatal("other session removed")
	}
	if e := s.End(":1.3"); e != nil {
		t.Fatal(e)
	}
	if len(fw.rules) != 1 {
		t.Fatal("unrelated caller changed rules")
	}
}
func TestReloadDoesNotRestoreRules(t *testing.T) {
	fw := &fakeFirewall{intact: false}
	s := New(nil, fw)
	s.sessions[":1.1"] = &lease{rules: []string{"stale"}}
	if s.End(":1.1") == nil {
		t.Fatal("expected reload error")
	}
	if len(fw.rules) != 0 {
		t.Fatal("restored stale rules")
	}
	select {
	case <-s.Fatal:
	default:
		t.Fatal("missing shutdown request")
	}
}
func TestCleanupFailureRetainsSocket(t *testing.T) {
	f, e := os.CreateTemp(t.TempDir(), "held")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	fw := &fakeFirewall{intact: true, err: fmt.Errorf("failure")}
	s := New(nil, fw)
	s.sessions[":1.1"] = &lease{files: []*os.File{f}}
	if s.End(":1.1") == nil {
		t.Fatal("expected failure")
	}
	if _, e := f.Stat(); e != nil {
		t.Fatal("released descriptor before rule removal")
	}
}
func TestAuthorizationBeforeValidation(t *testing.T) {
	f, e := os.Open(os.DevNull)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	fd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	s := New(nil, &fakeFirewall{intact: true})
	s.checkAuthorization = func(string) (uint32, error) { return 0, fmt.Errorf("denied") }
	eDB := s.Begin(dbus.Sender(":1.9"), dbus.Message{Body: []any{dbus.UnixFD(fd)}}, dbus.UnixFD(fd), dbus.UnixFD(fd), dbus.UnixFD(fd), dbus.UnixFD(fd))
	if eDB == nil || !strings.Contains(eDB.Error(), "denied") {
		t.Fatal(eDB)
	}
	// Begin owns received descriptors even on denial.
	if _, e = unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); e == nil {
		t.Fatal("descriptor leaked")
	}
}
func TestRejectNonSocket(t *testing.T) {
	var files []*os.File
	for i := 0; i < 4; i++ {
		f, e := os.Open(os.DevNull)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		files = append(files, f)
	}
	if _, e := validate(files, uint32(os.Getuid())); e == nil {
		t.Fatal("accepted non-sockets")
	}
}
func TestValidateRealSockets(t *testing.T) {
	if os.Getenv("DOUBLETAKE_NETNS_TEST") != "1" {
		t.Skip("run contrib/omarchy/networkd/run_network_test.py")
	}
	listener, e := net.Listen("tcp4", "192.0.2.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	conn, e := net.Dial("tcp4", listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	remote, e := listener.Accept()
	if e != nil {
		t.Fatal(e)
	}
	defer remote.Close()
	cf, e := conn.(*net.TCPConn).File()
	if e != nil {
		t.Fatal(e)
	}
	defer cf.Close()
	files := []*os.File{cf}
	for i := 0; i < 3; i++ {
		c, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 60100 + i})
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		f, e := c.File()
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		files = append(files, f)
	}
	l, e := validate(files, uint32(os.Getuid()))
	if e != nil {
		t.Fatal(e)
	}
	if len(l.rules) != 3 || !strings.Contains(l.rules[0], "-s 192.0.2.1/32 -d 192.0.2.1/32") {
		t.Fatal(l.rules)
	}
	testServiceBus(t, conn, files)
	files[2], files[3] = files[3], files[2]
	if _, e = validate(files, uint32(os.Getuid())); e == nil {
		t.Fatal("accepted wrong port order")
	}
}

func TestRejectNumericDescriptorForgery(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s := New(nil, &fakeFirewall{intact: true})
	s.checkAuthorization = func(string) (uint32, error) { return uint32(os.Getuid()), nil }
	fd := dbus.UnixFD(f.Fd())
	message := dbus.Message{Body: []any{int32(fd), int32(fd), int32(fd), int32(fd)}}
	if s.Begin(":1.9", message, fd, fd, fd, fd) == nil {
		t.Fatal("accepted forged descriptors")
	}
	if _, err = f.Stat(); err != nil {
		t.Fatal("closed unrelated process descriptor")
	}
}

func testServiceBus(t *testing.T, control net.Conn, files []*os.File) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "dbus-daemon", "--session", "--nofork", "--print-address=1")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); cmd.Wait() }()
	scanner := bufio.NewScanner(pipe)
	if !scanner.Scan() {
		t.Fatal("no bus address")
	}
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", scanner.Text())
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	fw := &fakeFirewall{intact: true}
	service := New(bus, fw)
	policy := &testPolicy{}
	if err = bus.Export(policy, "/org/freedesktop/PolicyKit1/Authority", "org.freedesktop.PolicyKit1.Authority"); err != nil {
		t.Fatal(err)
	}
	if _, err = bus.RequestName("org.freedesktop.PolicyKit1", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	if err = bus.Export(service, networkhelper.Path, networkhelper.BusName); err != nil {
		t.Fatal(err)
	}
	if _, err = bus.RequestName(networkhelper.BusName, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	var udp []net.PacketConn
	for _, f := range files[1:] {
		c, e := net.FilePacketConn(f)
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		udp = append(udp, c)
	}
	if rejected, e := networkhelper.Begin(ctx, control, udp); e == nil {
		rejected.Close()
		t.Fatal("Polkit denial was ignored")
	}
	service.mu.Lock()
	initialRules := len(fw.rules)
	service.mu.Unlock()
	if initialRules != 0 {
		t.Fatal("rules installed before authorization")
	}
	policy.allowed.Store(true)
	lease, err := networkhelper.Begin(ctx, control, udp)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	// A second caller shares these reserved sockets for this lifecycle test.
	second, err := dbus.ConnectSystemBus()
	if err != nil {
		t.Fatal(err)
	}
	err = second.Object(networkhelper.BusName, networkhelper.Path).Call(networkhelper.BusName+".Begin", 0,
		dbus.UnixFD(files[0].Fd()), dbus.UnixFD(files[1].Fd()), dbus.UnixFD(files[2].Fd()), dbus.UnixFD(files[3].Fd())).Err
	if err != nil {
		second.Close()
		t.Fatal(err)
	}
	service.mu.Lock()
	twoRules := len(fw.rules)
	service.mu.Unlock()
	if twoRules != 6 {
		t.Fatalf("expected two sessions: %d rules", twoRules)
	}
	second.Close() // Simulate a crash without End.
	superviseCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); service.Supervise(superviseCtx) }()
	defer func() { stop(); <-done; service.Shutdown(func() error { return nil }) }()
	deadline := time.Now().Add(4 * time.Second)
	for {
		service.mu.Lock()
		n := len(service.sessions)
		service.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("crashed caller was not cleaned up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	service.mu.Lock()
	n := len(fw.rules)
	service.mu.Unlock()
	if n != 3 {
		t.Fatal("other session's rules were removed")
	}
	lease.Close()
	service.mu.Lock()
	n = len(fw.rules)
	service.mu.Unlock()
	if n != 0 {
		t.Fatal("rules remain after final teardown")
	}
}

type policySubject struct {
	Kind    string
	Details map[string]dbus.Variant
}
type policyResult struct {
	Authorized bool
	Challenge  bool
	Details    map[string]string
}
type testPolicy struct{ allowed atomic.Bool }

func (p *testPolicy) CheckAuthorization(subject policySubject, action string, details map[string]string, flags uint32, cancellation string) (policyResult, *dbus.Error) {
	name, _ := subject.Details["name"].Value().(string)
	if subject.Kind != "system-bus-name" || !strings.HasPrefix(name, ":") || action != networkhelper.Action || flags != 0 {
		return policyResult{}, dbus.MakeFailedError(fmt.Errorf("invalid authorization subject"))
	}
	return policyResult{p.allowed.Load(), false, map[string]string{}}, nil
}
