package ufw

import (
	"net"
	"os"
	"testing"
	"time"
)

// This test runs only inside the isolated namespace prepared by the runner.
func TestKernelFirewall(t *testing.T) {
	if os.Getenv("DOUBLETAKE_NETNS_TEST") != "1" {
		t.Skip("run contrib/omarchy/networkd/run_network_test.py")
	}
	fw := &Backend{}
	if err := fw.Apply(nil); err != nil {
		t.Fatal(err)
	}
	defer Cleanup()
	receiver, err := net.ListenPacket("udp4", "192.0.2.1:60200")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sender, err := net.Dial("udp4", "192.0.2.1:60200")
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	receive := func(want bool) {
		t.Helper()
		sender.Write([]byte("probe"))
		receiver.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		b := make([]byte, 32)
		_, _, e := receiver.ReadFrom(b)
		if (e == nil) != want {
			t.Fatalf("receive=%v, want=%v: %v", e == nil, want, e)
		}
	}
	receive(false)
	rule := "-A " + Chain + " -s 192.0.2.1/32 -d 192.0.2.1/32 -p udp --dport 60200 -j ACCEPT"
	if err := fw.Apply([]string{rule}); err != nil {
		t.Fatal(err)
	}
	if !fw.Intact() {
		t.Fatal("fresh rules not intact")
	}
	receive(true)
	// An allowance for one receiver must not admit another source address.
	wrong, err := net.DialUDP("udp4", &net.UDPAddr{IP: net.ParseIP("192.0.2.2")}, &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 60200})
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	wrong.Write([]byte("wrong-peer"))
	receiver.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, e := receiver.ReadFrom(make([]byte, 32)); e == nil {
		t.Fatal("admitted wrong peer")
	}
	other, err := net.ListenPacket("udp4", "192.0.2.1:60201")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	probe, err := net.Dial("udp4", other.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	probe.Write([]byte("wrong-port"))
	other.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, e := other.ReadFrom(make([]byte, 32)); e == nil {
		t.Fatal("admitted wrong port")
	}
	if err := fw.Apply(nil); err != nil {
		t.Fatal(err)
	}
	receive(false)
	// Removing our permissions must preserve another administrator's rule.
	independent, err := net.ListenPacket("udp4", "192.0.2.1:60202")
	if err != nil {
		t.Fatal(err)
	}
	defer independent.Close()
	independentSender, err := net.Dial("udp4", independent.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer independentSender.Close()
	independentSender.Write([]byte("unrelated"))
	independent.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, e := independent.ReadFrom(make([]byte, 32)); e != nil {
		t.Fatalf("unrelated rule removed: %v", e)
	}
}
