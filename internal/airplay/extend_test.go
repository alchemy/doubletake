package airplay

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestParseExtendSize(t *testing.T) {
	for _, size := range []string{"", "1920x1080", "1280x720", "320x200", "8192x8192"} {
		if _, _, err := ParseExtendSize(size); err != nil {
			t.Errorf("%q: %v", size, err)
		}
	}
	for _, size := range []string{"1920", "1x1", "1921x1080", "0x0", "8194x1080", "1920x1080;exit", "1920x1080x60"} {
		if _, _, err := ParseExtendSize(size); err == nil {
			t.Errorf("accepted %q", size)
		}
	}
}

func TestExtendedOutputLifecycle(t *testing.T) {
	for _, mode := range []string{"lua", "legacy", "silent-mode-failure", "create-timeout", "collision"} {
		t.Run(mode, func(t *testing.T) {
			const name = "doubletake-test"
			existing := mode == "collision"
			created, removed, keywords := 0, 0, 0
			width, height := 0, 0
			run := func(ctx context.Context, args ...string) ([]byte, error) {
				switch args[0] {
				case "monitors":
					monitors := []hyprMonitor{{Name: "eDP-1", Width: 1920, Height: 1200}}
					if existing {
						monitors = append(monitors, hyprMonitor{Name: name, Width: width, Height: height, RefreshRate: 30, Scale: 1})
					}
					return json.Marshal(monitors)
				case "output":
					if args[1] == "create" {
						created++
						existing = true
						if mode == "create-timeout" {
							return nil, context.DeadlineExceeded
						}
					} else {
						if ctx.Err() != nil {
							t.Fatal("cleanup used canceled context")
						}
						if args[2] != name {
							t.Fatal("removed unrelated output")
						}
						removed++
						existing = false
					}
				case "eval":
					if mode == "legacy" {
						return []byte("unknown request"), nil
					}
					if mode != "silent-mode-failure" {
						width, height = 1280, 720
					}
				case "keyword":
					keywords++
					if mode != "silent-mode-failure" {
						width, height = 1280, 720
					}
				default:
					return nil, fmt.Errorf("unexpected command %v", args)
				}
				return []byte("ok"), nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			output, err := createExtendedOutput(ctx, run, name, 1280, 720, 30)
			success := mode == "lua" || mode == "legacy"
			if (err == nil) != success {
				t.Fatalf("err=%v, want success=%t", err, success)
			}
			if success {
				cancel()
				output.Close()
				output.Close()
			}
			if mode == "collision" {
				if created != 0 || removed != 0 {
					t.Fatal("touched existing output")
				}
			} else if created != 1 || removed != 1 {
				t.Fatalf("create/remove=%d/%d", created, removed)
			}
			if mode == "legacy" && keywords != 1 {
				t.Fatal("missing legacy fallback")
			}
		})
	}
}

func TestExtendedOutputPreparationClose(t *testing.T) {
	removed := 0
	output := &extendedOutput{name: "owned", run: func(context.Context, ...string) ([]byte, error) { removed++; return nil, nil }}
	p := &CapturePreparation{extendedOutput: output}
	p.Close()
	p.Close()
	if removed != 1 {
		t.Fatalf("removed %d times", removed)
	}
}

func TestExtendRequiresHyprland(t *testing.T) {
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	if _, err := prepareExtendedOutput(context.Background(), "1280x720", 30); err == nil || !strings.Contains(err.Error(), "Hyprland") {
		t.Fatalf("err=%v", err)
	}
}

// Opt-in smoke test creates and removes one real monitor; normal tests never
// alter the user's desktop or require a compositor.
func TestHyprlandExtendSmoke(t *testing.T) {
	if os.Getenv("DOUBLETAKE_TEST_HYPRLAND_EXTEND") != "1" {
		t.Skip("requires opt-in live Hyprland session")
	}
	before, err := readHyprMonitors(context.Background(), runHyprctl)
	if err != nil {
		t.Fatal(err)
	}
	output, err := prepareExtendedOutput(context.Background(), "1280x720", 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(output.Close)
	output.Close()
	after, err := readHyprMonitors(context.Background(), runHyprctl)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("monitor count changed: %d -> %d", len(before), len(after))
	}
	for _, m := range after {
		if m.Name == output.name {
			t.Fatal("virtual output survived cleanup")
		}
	}
}

func TestExtendedOutputCaptureStopIsConcurrentSafe(t *testing.T) {
	removed := 0
	output := &extendedOutput{name: "owned", run: func(context.Context, ...string) ([]byte, error) { removed++; return nil, nil }}
	done := make(chan struct{})
	close(done)
	capture := &ScreenCapture{extendedOutput: output, waitCh: done}
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); capture.Stop() }()
	}
	workers.Wait()
	if removed != 1 {
		t.Fatalf("output removed %d times", removed)
	}
}
