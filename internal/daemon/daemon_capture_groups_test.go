package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"doubletake/internal/airplay"
)

func TestNormalizedVideoCaptureKeyUsesEncodedEvenCanvas(t *testing.T) {
	for _, test := range []struct {
		name       string
		width      int
		height     int
		wantWidth  int
		wantHeight int
	}{
		{name: "1080p", width: 1920, height: 1080, wantWidth: 1920, wantHeight: 1080},
		{name: "odd dimensions", width: 1921, height: 1081, wantWidth: 1920, wantHeight: 1080},
		{name: "missing width", height: 720},
		{name: "missing height", width: 1280},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := normalizedVideoCaptureKey(test.width, test.height)
			if got.maxWidth != test.wantWidth || got.maxHeight != test.wantHeight {
				t.Fatalf("capture key = %dx%d, want %dx%d", got.maxWidth, got.maxHeight, test.wantWidth, test.wantHeight)
			}
		})
	}
}

func TestNormalizedVideoCaptureKeySeparatesResolvedCodecs(t *testing.T) {
	h264 := normalizedVideoCaptureKey(1920, 1080, airplay.VideoCodecH264)
	hevc := normalizedVideoCaptureKey(1920, 1080, airplay.VideoCodecHEVC)
	if h264 == hevc {
		t.Fatalf("H.264 and HEVC capture keys unexpectedly match: %+v", h264)
	}
	if h264.codec != airplay.VideoCodecH264 || hevc.codec != airplay.VideoCodecHEVC {
		t.Fatalf("capture key codecs = %s/%s", h264.codec, hevc.codec)
	}
}

func TestRemovingStreamStopsOnlyItsResolutionGroup(t *testing.T) {
	for _, first := range []videoCaptureKey{
		{maxWidth: 1920, maxHeight: 1080},
		{maxWidth: 1280, maxHeight: 720},
	} {
		second := videoCaptureKey{maxWidth: 1280, maxHeight: 720}
		if first == second {
			second = videoCaptureKey{maxWidth: 1920, maxHeight: 1080}
		}
		t.Run(captureKeyName(first)+" first", func(t *testing.T) {
			firstGroup := &videoCaptureGroup{key: first}
			secondGroup := &videoCaptureGroup{key: second}
			firstCtx, cancelFirst := context.WithCancel(context.Background())
			secondCtx, cancelSecond := context.WithCancel(context.Background())
			t.Cleanup(cancelFirst)
			t.Cleanup(cancelSecond)

			d := &Daemon{
				streams: map[string]*activeStream{
					"first":  {deviceIP: "first", captureGroup: firstGroup, cancelFn: cancelFirst},
					"second": {deviceIP: "second", captureGroup: secondGroup, cancelFn: cancelSecond},
				},
				captureGroups: map[videoCaptureKey]*videoCaptureGroup{
					first:  firstGroup,
					second: secondGroup,
				},
			}

			cleanup := d.detachStreamLocked("first")
			cleanup.run()
			if _, ok := d.captureGroups[first]; ok {
				t.Fatalf("removed stream left its %s capture group", captureKeyName(first))
			}
			if d.captureGroups[second] != secondGroup || d.streams["second"] == nil {
				t.Fatalf("removing %s disturbed active %s group", captureKeyName(first), captureKeyName(second))
			}
			select {
			case <-firstCtx.Done():
			default:
				t.Fatal("removed stream context was not cancelled")
			}
			select {
			case <-secondCtx.Done():
				t.Fatal("other resolution stream was cancelled")
			default:
			}
		})
	}
}

func TestCaptureGroupLivesUntilItsLastSharedStreamLeaves(t *testing.T) {
	key := videoCaptureKey{maxWidth: 1280, maxHeight: 720}
	group := &videoCaptureGroup{key: key}
	d := &Daemon{
		streams: map[string]*activeStream{
			"one": {deviceIP: "one", captureGroup: group},
			"two": {deviceIP: "two", captureGroup: group},
		},
		captureGroups: map[videoCaptureKey]*videoCaptureGroup{key: group},
	}

	cleanup := d.detachStreamLocked("one")
	cleanup.run()
	if d.captureGroups[key] != group {
		t.Fatal("shared capture group stopped while one stream remained")
	}
	cleanup = d.detachStreamLocked("two")
	cleanup.run()
	if len(d.captureGroups) != 0 {
		t.Fatalf("last shared stream left %d capture groups", len(d.captureGroups))
	}
}

func captureKeyName(key videoCaptureKey) string {
	return fmt.Sprintf("%dx%d", key.maxWidth, key.maxHeight)
}

func TestDisconnectAndShutdownCleanupRunWithoutDaemonMutex(t *testing.T) {
	for _, test := range []struct {
		name   string
		invoke func(*Daemon)
	}{
		{
			name: "targeted disconnect",
			invoke: func(d *Daemon) {
				d.handleDisconnect(Request{Cmd: "disconnect", Target: "target"})
			},
		},
		{
			name: "disconnect all",
			invoke: func(d *Daemon) {
				d.handleDisconnect(Request{Cmd: "disconnect"})
			},
		},
		{
			name: "shutdown",
			invoke: func(d *Daemon) {
				d.Shutdown()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			entry := &activeStream{
				deviceIP: "target",
				cancelFn: func() {
					close(started)
					<-release
				},
			}
			d := &Daemon{
				cfg:           Config{SocketPath: filepath.Join(t.TempDir(), "daemon.sock")},
				streams:       map[string]*activeStream{"target": entry},
				captureGroups: make(map[videoCaptureKey]*videoCaptureGroup),
			}

			done := make(chan struct{})
			go func() {
				test.invoke(d)
				close(done)
			}()

			waitForCleanupBlock(t, started, release, done)
			assertDaemonMutexAvailable(t, d, release, done, func() {
				if len(d.streams) != 0 {
					t.Errorf("cleanup started before stream was detached: %+v", d.streams)
				}
			})
		})
	}
}

func TestCaptureFailureCleanupRunsWithoutDaemonMutex(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	key := videoCaptureKey{maxWidth: 1920, maxHeight: 1080}
	group := &videoCaptureGroup{key: key}
	entry := &activeStream{
		deviceIP:     "target",
		captureGroup: group,
		cancelFn: func() {
			close(started)
			<-release
		},
	}
	d := &Daemon{
		streams:       map[string]*activeStream{"target": entry},
		captureGroups: map[videoCaptureKey]*videoCaptureGroup{key: group},
	}

	done := make(chan struct{})
	go func() {
		d.finishCaptureGroup(group, nil, errors.New("capture stopped"))
		close(done)
	}()

	waitForCleanupBlock(t, started, release, done)
	assertDaemonMutexAvailable(t, d, release, done, func() {
		if len(d.streams) != 0 || len(d.captureGroups) != 0 {
			t.Errorf("capture failure cleanup was not detached: streams=%+v groups=%+v", d.streams, d.captureGroups)
		}
		if d.lastError == "" {
			t.Error("capture failure did not publish its error before cleanup")
		}
	})
}

func TestShutdownWaitsForConcurrentDetachedCleanup(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	d := &Daemon{
		cfg: Config{SocketPath: filepath.Join(t.TempDir(), "daemon.sock")},
		streams: map[string]*activeStream{
			"target": {
				deviceIP: "target",
				cancelFn: func() {
					close(started)
					<-release
				},
			},
		},
		captureGroups: make(map[videoCaptureKey]*videoCaptureGroup),
	}

	disconnectDone := make(chan struct{})
	go func() {
		d.handleDisconnect(Request{Cmd: "disconnect", Target: "target"})
		close(disconnectDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("disconnect cleanup did not start")
	}

	shutdownDone := make(chan struct{})
	go func() {
		d.Shutdown()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
		close(release)
		<-disconnectDone
		t.Fatal("Shutdown returned before detached cleanup completed")
	case <-time.After(25 * time.Millisecond):
	}

	close(release)
	select {
	case <-disconnectDone:
	case <-time.After(time.Second):
		t.Fatal("disconnect cleanup did not finish")
	}
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not finish after detached cleanup")
	}
}

func waitForCleanupBlock(t *testing.T, started, release, done chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("cleanup did not reach the deliberately blocking operation")
	}
}

func assertDaemonMutexAvailable(t *testing.T, d *Daemon, release, done chan struct{}, check func()) {
	t.Helper()
	locked := make(chan struct{})
	go func() {
		d.mu.Lock()
		check()
		d.mu.Unlock()
		close(locked)
	}()

	select {
	case <-locked:
		close(release)
		<-done
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("daemon mutex remained locked during blocking cleanup")
	}
}

func TestExtendedDesktopCaptureGroupsArePerReceiver(t *testing.T) {
	first := &activeStream{deviceIP: "192.0.2.1"}
	second := &activeStream{deviceIP: "192.0.2.2"}
	d := &Daemon{}
	key := func(entry *activeStream) videoCaptureKey {
		return d.videoCaptureKey(entry, 1920, 1080, airplay.VideoCodecH264)
	}
	if key(first) != key(second) {
		t.Fatal("mirror sessions should share their capture")
	}
	d.cfg.ExtendSize = "1920x1080"
	if key(first) == key(second) {
		t.Fatal("extended desktops must not share a capture")
	}
	sameReceiver := &activeStream{deviceIP: first.deviceIP}
	if key(first) != key(sameReceiver) {
		t.Fatal("capture identity must remain stable across setup retry")
	}
}

func TestSessionModeDefaultsAndOverrides(t *testing.T) {
	for _, tt := range []struct {
		mode, size, defaultSize, wantMode, wantSize string
		invalid                                     bool
	}{
		{wantMode: "mirror"},
		{defaultSize: "1280x720", wantMode: "extend", wantSize: "1280x720"},
		{mode: "mirror", defaultSize: "1280x720", wantMode: "mirror"},
		{mode: "extend", wantMode: "extend", wantSize: "1920x1080"},
		{mode: "extend", size: "1280x720", wantMode: "extend", wantSize: "1280x720"},
		{mode: "invalid", invalid: true},
		{mode: "mirror", size: "1280x720", invalid: true},
		{mode: "extend", size: "bad", invalid: true},
	} {
		mode, size, err := resolveSessionMode(tt.mode, tt.size, tt.defaultSize)
		if (err != nil) != tt.invalid || mode != tt.wantMode || size != tt.wantSize {
			t.Fatalf("%+v: got %s/%s, %v", tt, mode, size, err)
		}
	}
}

func TestMixedSessionCaptureIsolation(t *testing.T) {
	d := &Daemon{cfg: Config{ExtendSize: "1920x1080"}}
	mirror := &activeStream{deviceIP: "192.0.2.1", mode: "mirror"}
	extend := &activeStream{deviceIP: "192.0.2.2", mode: "extend", extendSize: "1280x720"}
	other := &activeStream{deviceIP: "192.0.2.3", mode: "extend", extendSize: "1280x720"}
	if d.streamExtendSize(mirror) != "" || d.streamExtendSize(extend) != "1280x720" {
		t.Fatal("per-session modes did not override daemon default")
	}
	key := func(e *activeStream) videoCaptureKey { return d.videoCaptureKey(e, 1280, 720, airplay.VideoCodecH264) }
	if key(mirror) == key(extend) || key(extend) == key(other) {
		t.Fatal("independent sources share a capture")
	}
	d.streams = map[string]*activeStream{mirror.deviceIP: mirror, extend.deviceIP: extend}
	status := d.handleStatus()
	if status.Streams[0].Mode != "mirror" || status.Streams[1].Mode != "extend" || status.Streams[1].ExtendSize != "1280x720" {
		t.Fatalf("wrong modes in status: %+v", status.Streams)
	}
}
