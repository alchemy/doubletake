package daemon

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestStatusCapabilitiesIndependentOfSessionAndEnvironment(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	for _, state := range []State{StateIdle, StateConnecting, StateStreaming, StatePINRequired} {
		t.Run(string(state), func(t *testing.T) {
			d := &Daemon{lastError: "previous connection failed"}
			if state != StateIdle {
				d.streams = map[string]*activeStream{"192.0.2.1": {deviceIP: "192.0.2.1", state: state, mode: "mirror"}}
			}
			response := d.handleStatus()
			if response.State != state || response.Error == "" {
				t.Fatalf("unexpected status: %+v", response)
			}
			assertCapabilitiesJSON(t, response)
			d.mu.Lock()
			failed := d.statusResponseLocked(false, "command failed")
			d.mu.Unlock()
			if failed.OK {
				t.Fatal("expected unsuccessful response")
			}
			assertCapabilitiesJSON(t, failed)
		})
	}
}

func assertCapabilitiesJSON(t *testing.T, response Response) {
	t.Helper()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"session_modes":    []interface{}{"mirror", "extend"},
		"per_session_mode": true,
		"extend_backends":  []interface{}{"hyprland"},
	}
	if !reflect.DeepEqual(decoded["capabilities"], want) {
		t.Fatalf("capabilities=%#v", decoded["capabilities"])
	}
	// Existing clients can continue decoding their original fields.
	var legacy struct {
		OK    bool  `json:"ok"`
		State State `json:"state"`
	}
	if err := json.Unmarshal(encoded, &legacy); err != nil || legacy.OK != response.OK || legacy.State != response.State {
		t.Fatalf("legacy decode failed: %v", err)
	}
}

func TestLegacyResponseHasUnknownCapabilities(t *testing.T) {
	var response Response
	if err := json.Unmarshal([]byte(`{"ok":true,"state":"idle"}`), &response); err != nil {
		t.Fatal(err)
	}
	if response.Capabilities != nil {
		t.Fatal("missing capabilities must remain unknown")
	}
}

func TestStatusCapabilitiesDoNotShareMutableSlices(t *testing.T) {
	d := &Daemon{}
	first := d.handleStatus()
	first.Capabilities.SessionModes[0] = "changed"
	first.Capabilities.ExtendBackends[0] = "changed"
	assertCapabilitiesJSON(t, d.handleStatus())
}
