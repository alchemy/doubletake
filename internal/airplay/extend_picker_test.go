package airplay

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPickerConfigPreservesFallback(t *testing.T) {
	original := "screencopy {\n    allow_token_by_default = true\n    custom_picker_binary = hyprland-preview-share-picker\n}\n"
	next, fallback, err := pickerConfig(original, "/home/test/picker.sh")
	if err != nil || fallback != "hyprland-preview-share-picker" {
		t.Fatalf("%q %v", fallback, err)
	}
	if !strings.HasPrefix(next, original) {
		t.Fatal("lost existing config")
	}
	again, _, err := pickerConfig(next, "/home/test/picker.sh")
	if err != nil || again != next {
		t.Fatal("installation is not idempotent")
	}
	restored, err := stripPickerBlock(next + "# later user edit\n")
	if err != nil || restored != original+"# later user edit\n" {
		t.Fatal("lost user edits")
	}
	if _, _, err := pickerConfig(pickerBegin, "/x"); err == nil {
		t.Fatal("accepted incomplete block")
	}
	if _, _, err := pickerConfig("# --- waycast: managed block, restored on disconnect ---", "/x"); err == nil {
		t.Fatal("accepted active Waycast override")
	}
}

func TestExtendPickerOneShotAndFallback(t *testing.T) {
	for _, mode := range []string{"valid", "expired", "dead-owner", "invalid-output", "missing"} {
		t.Run(mode, func(t *testing.T) {
			runtime := t.TempDir()
			t.Setenv("XDG_RUNTIME_DIR", runtime)
			dir := filepath.Join(runtime, "doubletake")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			fallback := filepath.Join(runtime, "normal picker")
			if err := os.WriteFile(fallback, []byte("#!/bin/sh\nprintf 'NORMAL:%s\\n' \"$1\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(runtime, "picker.sh")
			if err := os.WriteFile(script, []byte(extendPickerScript(fallback)), 0700); err != nil {
				t.Fatal(err)
			}
			owner := os.Getpid()
			deadline := time.Now().Add(time.Minute).Unix()
			output := "doubletake-0123456789abcdef"
			if mode == "expired" {
				deadline = 1
			}
			if mode == "dead-owner" {
				owner = 2147483647
			}
			if mode == "invalid-output" {
				output = "eDP-1"
			}
			marker := filepath.Join(dir, "portal-target")
			if mode != "missing" {
				if err := os.WriteFile(marker, []byte(fmt.Sprintf("%d %d %s\n", owner, deadline, output)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			out, err := exec.Command(script, "argument").CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			want := "NORMAL:argument\n"
			if mode == "valid" {
				want = "[SELECTION]allow-token/screen:" + output + "\n"
			}
			if string(out) != want {
				t.Fatalf("got %q want %q", out, want)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("marker survived consumption")
			}
			out, err = exec.Command(script, "argument").CombinedOutput()
			if err != nil || string(out) != "NORMAL:argument\n" {
				t.Fatalf("second request: %q %v", out, err)
			}
		})
	}
}

func TestExtendPickerLockCancellation(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	first, _, err := lockExtendPicker(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if second, _, err := lockExtendPicker(ctx); err == nil {
		second.Close()
		t.Fatal("concurrent capture acquired lock")
	}
}
