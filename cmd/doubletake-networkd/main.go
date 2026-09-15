package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"doubletake/internal/networkd"
	"doubletake/internal/networkd/ufw"
	"doubletake/internal/networkhelper"
	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	if len(os.Args) > 1 && os.Args[1] == "--cleanup" {
		return ufw.Cleanup()
	}
	lock, err := os.OpenFile("/run/doubletake-networkd.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("network helper already running: %w", err)
	}
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer bus.Close()
	fw := &ufw.Backend{}
	if err := fw.Apply(nil); err != nil {
		return err
	}
	s := networkd.New(bus, fw)
	defer func() {
		if err := s.Shutdown(ufw.Cleanup); err != nil {
			log.Printf("cleanup failed: %v", err)
		}
	}()
	if err = bus.Export(s, networkhelper.Path, networkhelper.BusName); err != nil {
		return err
	}
	// Claim the activation name only when the firewall and exported API are ready.
	reply, err := bus.RequestName(networkhelper.BusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("network helper bus name already owned")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.Supervise(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-s.Fatal:
		return err
	}
}
