package airplay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ParseExtendSize validates the virtual desktop canvas, independently of the
// receiver's encoded canvas. An empty size disables extend mode.
func ParseExtendSize(size string) (int, int, error) {
	if size == "" {
		return 0, 0, nil
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("extend size must be WIDTHxHEIGHT")
	}
	w, e1 := strconv.Atoi(parts[0])
	h, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil || w < 320 || h < 200 || w > 8192 || h > 8192 || w%2 != 0 || h%2 != 0 {
		return 0, 0, fmt.Errorf("extend size must have even dimensions within 320x200 and 8192x8192")
	}
	return w, h, nil
}

type hyprCommand func(context.Context, ...string) ([]byte, error)

type extendedOutput struct {
	name string
	run  hyprCommand
	once sync.Once
}

func runHyprctl(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "hyprctl", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("hyprctl %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return out, nil
}

func (o *extendedOutput) Close() {
	if o == nil {
		return
	}
	o.once.Do(func() {
		// Teardown must still work after the capture context has been cancelled.
		if _, err := o.run(context.Background(), "output", "remove", o.name); err != nil {
			log.Printf("[EXTEND] cannot remove %s: %v; remove it with hyprctl output remove %s", o.name, err, o.name)
		}
	})
}

type hyprMonitor struct {
	Name        string  `json:"name"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Disabled    bool    `json:"disabled"`
	RefreshRate float64 `json:"refreshRate"`
	Scale       float64 `json:"scale"`
}

func readHyprMonitors(ctx context.Context, run hyprCommand) ([]hyprMonitor, error) {
	out, err := run(ctx, "monitors", "all", "-j")
	if err != nil {
		return nil, err
	}
	var monitors []hyprMonitor
	if err := json.Unmarshal(out, &monitors); err != nil {
		return nil, fmt.Errorf("decode Hyprland monitors: %w", err)
	}
	return monitors, nil
}

func prepareExtendedOutput(ctx context.Context, size string, fps int) (*extendedOutput, error) {
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" || os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil, fmt.Errorf("extend mode currently requires a running Hyprland Wayland session")
	}
	w, h, err := ParseExtendSize(size)
	if err != nil {
		return nil, err
	}
	if fps <= 0 || fps > 240 {
		return nil, fmt.Errorf("extend mode requires FPS between 1 and 240")
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	return createExtendedOutput(ctx, runHyprctl, "doubletake-"+hex.EncodeToString(id[:]), w, h, fps)
}

func createExtendedOutput(ctx context.Context, run hyprCommand, name string, w, h, fps int) (_ *extendedOutput, err error) {
	monitors, err := readHyprMonitors(ctx, run)
	if err != nil {
		return nil, err
	}
	for _, m := range monitors {
		if m.Name == name {
			return nil, fmt.Errorf("output %s already exists", name)
		}
	}
	o := &extendedOutput{name: name, run: run}
	// The name is unique and known before creation, so even a timeout after the
	// compositor creates the output can be rolled back without touching others.
	success := false
	defer func() {
		if !success {
			o.Close()
		}
	}()
	if _, err := run(ctx, "output", "create", "headless", name); err != nil {
		return nil, err
	}
	mode := fmt.Sprintf("%dx%d@%d", w, h, fps)
	// Current Hyprland uses Lua. Older releases use the keyword interface. Do
	// not trust a zero exit code: both interfaces can return textual errors.
	lua := fmt.Sprintf("hl.monitor({output = %q, mode = %q, position = \"auto\", scale = 1})", name, mode)
	_, luaErr := run(ctx, "eval", lua)
	matches := func() bool {
		monitors, err := readHyprMonitors(ctx, run)
		if err != nil {
			return false
		}
		for _, m := range monitors {
			if m.Name == name && m.Width == w && m.Height == h && !m.Disabled && math.Abs(m.RefreshRate-float64(fps)) < 1 && m.Scale == 1 {
				return true
			}
		}
		return false
	}
	if luaErr != nil || !matches() {
		if _, err := run(ctx, "keyword", "monitor", name+","+mode+",auto,1"); err != nil {
			return nil, err
		}
	}
	if !matches() {
		return nil, fmt.Errorf("Hyprland did not apply %s to output %s", mode, name)
	}
	success = true
	log.Printf("[EXTEND] created %s (%s). Selecting this monitor automatically for sharing.", name, mode)
	return o, nil
}
