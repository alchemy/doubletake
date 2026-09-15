// Package networkd implements the privileged session firewall broker.
package networkd

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"doubletake/internal/networkd/ufw"
	"doubletake/internal/networkhelper"
	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

type Firewall interface {
	Apply([]string) error
	Intact() bool
}
type lease struct {
	files []*os.File
	rules []string
	index int
	local string
}
type Service struct {
	checkAuthorization func(string) (uint32, error)
	stopped            bool
	Bus                *dbus.Conn
	Firewall           Firewall
	mu                 sync.Mutex
	sessions           map[string]*lease
	Fatal              chan error
}

func New(bus *dbus.Conn, fw Firewall) *Service {
	return &Service{Bus: bus, Firewall: fw, sessions: make(map[string]*lease), Fatal: make(chan error, 1)}
}
func fail(e error) *dbus.Error { return dbus.MakeFailedError(e) }
func (s *Service) fatal(e error) {
	select {
	case s.Fatal <- e:
	default:
	}
}
func (l *lease) close() {
	for _, f := range l.files {
		f.Close()
	}
}
func (s *Service) apply() error {
	var rules []string
	for _, l := range s.sessions {
		rules = append(rules, l.rules...)
	}
	return s.Firewall.Apply(rules)
}
func (s *Service) authorize(owner string) (uint32, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	subject := struct {
		Kind    string
		Details map[string]dbus.Variant
	}{"system-bus-name", map[string]dbus.Variant{"name": dbus.MakeVariant(owner)}}
	var result struct {
		Authorized bool
		Challenge  bool
		Details    map[string]string
	}
	err := s.Bus.Object("org.freedesktop.PolicyKit1", "/org/freedesktop/PolicyKit1/Authority").CallWithContext(ctx, "org.freedesktop.PolicyKit1.Authority.CheckAuthorization", 0, subject, networkhelper.Action, map[string]string{}, uint32(0), "").Store(&result)
	if err != nil {
		return 0, err
	}
	if !result.Authorized {
		return 0, fmt.Errorf("active local session authorization required")
	}
	var uid uint32
	err = s.Bus.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetConnectionUnixUser", 0, owner).Store(&uid)
	return uid, err
}
func (s *Service) Begin(sender dbus.Sender, message dbus.Message, control, timing, audioControl, audioData dbus.UnixFD) *dbus.Error {
	// Received descriptors belong to this invocation, including rejected calls.
	var files []*os.File
	seen := make(map[dbus.UnixFD]bool)
	repeated := false
	malformed := len(message.Body) != 4
	for _, value := range message.Body {
		// godbus.Store permits numeric conversions. Only an actual received UnixFD
		// may be treated as a descriptor in this privileged process.
		fd, valid := value.(dbus.UnixFD)
		if !valid {
			malformed = true
			continue
		}
		if seen[fd] {
			repeated = true
			continue
		}
		seen[fd] = true
		files = append(files, os.NewFile(uintptr(fd), "session-socket"))
	}
	keep := false
	defer func() {
		if !keep {
			for _, f := range files {
				if f != nil {
					f.Close()
				}
			}
		}
	}()
	authorize := s.checkAuthorization
	if authorize == nil {
		authorize = s.authorize
	}
	uid, err := authorize(string(sender))
	if err != nil {
		return fail(err)
	}
	if malformed {
		return fail(fmt.Errorf("expected received Unix file descriptors"))
	}
	if repeated {
		return fail(fmt.Errorf("duplicate socket descriptors"))
	}
	l, err := validate(files, uid)
	if err != nil {
		return fail(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return fail(fmt.Errorf("helper is shutting down"))
	}
	if len(s.sessions) >= 32 {
		return fail(fmt.Errorf("session limit reached"))
	}
	if _, ok := s.sessions[string(sender)]; ok {
		return fail(fmt.Errorf("caller already has a session"))
	}
	if !s.Firewall.Intact() {
		return fail(fmt.Errorf("firewall changed; restart helper before reconnecting"))
	}
	s.sessions[string(sender)] = l
	if err = s.apply(); err != nil {
		keep = true // Retain ports until process cleanup even if apply partly succeeded.
		s.fatal(err)
		return fail(err)
	}
	keep = true
	return nil
}
func (s *Service) End(sender dbus.Sender) *dbus.Error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.sessions[string(sender)]
	if !ok {
		return nil
	}
	if !s.Firewall.Intact() {
		err := fmt.Errorf("firewall changed during teardown")
		s.fatal(err)
		return fail(err)
	}
	delete(s.sessions, string(sender))
	if err := s.apply(); err != nil {
		s.sessions[string(sender)] = l
		s.fatal(err)
		return fail(err)
	}
	l.close()
	return nil
}
func (s *Service) Alive(sender dbus.Sender) (bool, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[string(sender)]
	return ok, nil
}
func (s *Service) Shutdown(cleanup func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	err := cleanup()
	for _, l := range s.sessions {
		l.close()
	}
	clear(s.sessions)
	return err
}

// Supervise polls unique bus-name ownership and interface identity. Any external
// firewall change ends all sessions; stale permissions are never reinstalled.
func (s *Service) Supervise(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			if !s.Firewall.Intact() {
				s.mu.Unlock()
				s.fatal(fmt.Errorf("UFW rules changed"))
				return
			}
			var gone []string
			for owner, l := range s.sessions {
				c, cancel := context.WithTimeout(ctx, 2*time.Second)
				var alive bool
				err := s.Bus.BusObject().CallWithContext(c, "org.freedesktop.DBus.NameHasOwner", 0, owner).Store(&alive)
				cancel()
				if err != nil || !alive || !interfaceHasAddress(l.index, l.local) {
					gone = append(gone, owner)
				}
			}
			s.mu.Unlock()
			for _, owner := range gone {
				s.End(dbus.Sender(owner))
			}
		}
	}
}
func interfaceHasAddress(index int, local string) bool {
	iface, err := net.InterfaceByIndex(index)
	if err != nil {
		return false
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		ip, _, e := net.ParseCIDR(a.String())
		if e == nil && ip.String() == local {
			return true
		}
	}
	return false
}
func ipv4(sa unix.Sockaddr) (net.IP, int, error) {
	switch a := sa.(type) {
	case *unix.SockaddrInet4:
		return net.IP(a.Addr[:]), a.Port, nil
	case *unix.SockaddrInet6:
		ip := net.IP(a.Addr[:]).To4()
		if ip != nil {
			return ip, a.Port, nil
		}
	}
	return nil, 0, fmt.Errorf("managed networking currently requires IPv4")
}
func validate(files []*os.File, uid uint32) (*lease, error) {
	if len(files) != 4 {
		return nil, fmt.Errorf("expected one TCP and three UDP sockets")
	}
	for i, f := range files {
		if f == nil {
			return nil, fmt.Errorf("missing socket")
		}
		fd := int(f.Fd())
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return nil, err
		}
		if stat.Uid != uid {
			return nil, fmt.Errorf("socket belongs to another user")
		}
		typ, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
		if err != nil {
			return nil, err
		}
		want := unix.SOCK_DGRAM
		if i == 0 {
			want = unix.SOCK_STREAM
		}
		if typ != want {
			return nil, fmt.Errorf("unexpected socket type")
		}
		proto, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PROTOCOL)
		if err != nil {
			return nil, err
		}
		wantProto := unix.IPPROTO_UDP
		if i == 0 {
			wantProto = unix.IPPROTO_TCP
		}
		if proto != wantProto {
			return nil, fmt.Errorf("unexpected socket protocol")
		}
	}
	remote, err := unix.Getpeername(int(files[0].Fd()))
	if err != nil {
		return nil, err
	}
	peer, _, err := ipv4(remote)
	if err != nil {
		return nil, err
	}
	localSA, err := unix.Getsockname(int(files[0].Fd()))
	if err != nil {
		return nil, err
	}
	local, _, err := ipv4(localSA)
	if err != nil {
		return nil, err
	}
	if !peer.IsGlobalUnicast() || !local.IsGlobalUnicast() {
		return nil, fmt.Errorf("expected unicast LAN endpoints")
	}
	var iface net.Interface
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, candidate := range interfaces {
		if interfaceHasAddress(candidate.Index, local.String()) {
			iface = candidate
			break
		}
	}
	if iface.Index == 0 {
		return nil, fmt.Errorf("local interface not found")
	}
	// Interface names are interpolated into iptables-restore; allow only safe bytes.
	for _, c := range iface.Name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return nil, fmt.Errorf("unsupported interface name")
		}
	}
	l := &lease{files: files, index: iface.Index, local: local.String()}
	base := 0
	for i, f := range files[1:] {
		sa, e := unix.Getsockname(int(f.Fd()))
		if e != nil {
			return nil, e
		}
		var ip net.IP
		var port int
		// Go's wildcard UDP listeners may use the dual-stack IPv6 wildcard.
		if a, ok := sa.(*unix.SockaddrInet6); ok && net.IP(a.Addr[:]).IsUnspecified() {
			ip = net.IPv4zero
			port = a.Port
		} else {
			ip, port, e = ipv4(sa)
			if e != nil {
				return nil, e
			}
		}
		if port < 1024 || (!ip.IsUnspecified() && !ip.Equal(local)) {
			return nil, fmt.Errorf("invalid local UDP endpoint")
		}
		if i == 0 {
			base = port
		} else if port != base+i {
			return nil, fmt.Errorf("UDP ports must be consecutive")
		}
		l.rules = append(l.rules, fmt.Sprintf("-A %s -i %s -s %s/32 -d %s/32 -p udp --dport %d -j ACCEPT", ufw.Chain, iface.Name, peer.String(), local.String(), port))
	}
	return l, nil
}
