package airplay

// Hyprland's custom picker hook, inspired by Waycast's one-shot portal target.
// The hook has no requester identity: the short arming window cannot exclude
// requests from unrelated applications. Do not leave a marker armed in-session.
import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const pickerBegin = "# BEGIN doubletake extend picker"
const pickerEnd = "# END doubletake extend picker"

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func extendPickerScript(fallback string) string {
	return `#!/bin/bash
# Doubletake: consume at most one short-lived extend request, otherwise use
# the previously configured picker. No application identity is supplied by XDPH.
if [[ -n ${XDG_RUNTIME_DIR:-} ]]; then
    marker="$XDG_RUNTIME_DIR/doubletake/portal-target"
    claim="$marker.$$"
    if mv -T -- "$marker" "$claim" 2>/dev/null; then
        read -r owner deadline output < "$claim"
        rm -f -- "$claim"
        if [[ $owner =~ ^[0-9]+$ && $deadline =~ ^[0-9]+$ && $output =~ ^doubletake-[0-9a-f]{16}$ ]] &&
           (( $(date +%s) <= deadline )) && kill -0 "$owner" 2>/dev/null; then
            printf '[SELECTION]allow-token/screen:%s\n' "$output"
            exit 0
        fi
    fi
fi
exec ` + shellQuote(fallback) + ` "$@"
`
}

func stripPickerBlock(s string) (string, error) {
	start := strings.Index(s, pickerBegin)
	if start < 0 {
		return s, nil
	}
	end := strings.Index(s[start:], pickerEnd)
	if end < 0 {
		return "", fmt.Errorf("incomplete doubletake picker block in xdph.conf")
	}
	end += start + len(pickerEnd)
	if end < len(s) && s[end] == '\n' {
		end++
	}
	return s[:start] + s[end:], nil
}

var pickerAssignment = regexp.MustCompile(`(?m)^\s*custom_picker_binary\s*=\s*([^\n#]+)`)
var pickerExecutable = regexp.MustCompile(`^[a-zA-Z0-9_./+@ -]+$`)

func pickerConfig(original, script string) (string, string, error) {
	base, err := stripPickerBlock(original)
	if err != nil {
		return "", "", err
	}
	// Installing over Waycast's temporary wrapper can form a fallback cycle.
	if strings.Contains(base, "# --- waycast: managed block") {
		return "", "", fmt.Errorf("finish the Waycast session before installing the doubletake picker")
	}
	fallback := "hyprland-share-picker"
	matches := pickerAssignment.FindAllStringSubmatch(base, -1)
	if len(matches) > 0 {
		fallback = strings.Trim(strings.TrimSpace(matches[len(matches)-1][1]), `"`)
	}
	if !pickerExecutable.MatchString(fallback) || fallback == script || strings.HasPrefix(fallback, "-") {
		return "", "", fmt.Errorf("unsupported custom picker executable %q", fallback)
	}
	// XDPH also checks the executable path directly; shell quoting here is
	// not portable across versions. Reject paths needing shell interpretation.
	if !regexp.MustCompile(`^[a-zA-Z0-9_./+@-]+$`).MatchString(script) {
		return "", "", fmt.Errorf("unsupported picker installation path")
	}
	if base != "" && !strings.HasSuffix(base, "\n") {
		base += "\n"
	}
	return base + pickerBegin + "\nscreencopy {\n    custom_picker_binary = " + script + "\n}\n" + pickerEnd + "\n", fallback, nil
}

func writePickerFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".doubletake-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// Install once, not on every connection: restarting XDPH terminates active
// captures. Keep a permanent pass-through wrapper so subsequent connections
// and disconnects require no portal restart or config rewrite.
func ensureExtendPicker(ctx context.Context) error {
	configHome, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(dataHome, "doubletake")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	script := filepath.Join(dir, "hyprland-picker.sh")
	config := filepath.Join(configHome, "hypr", "xdph.conf")
	original, err := os.ReadFile(config)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next, fallback, err := pickerConfig(string(original), script)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath(fallback); err != nil {
		return fmt.Errorf("existing sharing picker is not an executable: %w", err)
	}
	if err := writePickerFile(script, []byte(extendPickerScript(fallback)), 0700); err != nil {
		return err
	}
	if next == string(original) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		return err
	}
	if existed {
		backup, err := os.OpenFile(config+".doubletake-backup", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, err = backup.Write(original)
			closeErr := backup.Close()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil && !os.IsExist(err) {
			return err
		}
	}
	if err := writePickerFile(config, []byte(next), 0600); err != nil {
		return err
	}
	log.Printf("[EXTEND] installed picker wrapper (fallback: %s); restarting Hyprland sharing portal once", fallback)
	restartCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(restartCtx, "systemctl", "--user", "restart", "xdg-desktop-portal-hyprland.service").CombinedOutput()
	if err != nil {
		// Roll back only if nobody edited the file during restart.
		current, readErr := os.ReadFile(config)
		if readErr == nil && string(current) == next {
			if existed {
				_ = writePickerFile(config, original, 0600)
			} else {
				_ = os.Remove(config)
			}
		}
		return fmt.Errorf("restart Hyprland portal: %w: %s", err, out)
	}
	return nil
}

// Serialize doubletake portal acquisitions across processes. External apps do
// not use this lock and can still race the identity-free custom-picker hook.
func lockExtendPicker(ctx context.Context) (*os.File, string, error) {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return nil, "", fmt.Errorf("Hyprland capture requires XDG_RUNTIME_DIR")
	}
	dir := filepath.Join(runtimeDir, "doubletake")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, "", err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "portal.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, "", err
	}
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return lock, filepath.Join(dir, "portal-target"), nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			lock.Close()
			return nil, "", err
		}
		select {
		case <-ctx.Done():
			lock.Close()
			return nil, "", ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
