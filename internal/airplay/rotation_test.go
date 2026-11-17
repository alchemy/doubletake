package airplay

import (
	"strings"
	"testing"
)

func TestCaptureRotation(t *testing.T) {
	for _, direction := range []string{"90l", "90r", "180"} {
		t.Setenv("DOUBLETAKE_CAPTURE_DIRECTION", direction)
		if got := captureRotation(CaptureConfig{}); got != direction {
			t.Fatalf("got %q, want %q", got, direction)
		}
		if got := captureRotation(CaptureConfig{ExtendSize: "1920x1080"}); got != "identity" {
			t.Fatalf("extend rotated: %q", got)
		}
	}
	t.Setenv("DOUBLETAKE_CAPTURE_DIRECTION", "90l ! fakesink")
	if got := captureRotation(CaptureConfig{}); got != "identity" {
		t.Fatalf("accepted invalid direction: %q", got)
	}
}

func TestVARotationStaysOnGPU(t *testing.T) {
	encoder := encoderResult{parts: gstStage{"vah264enc"}, rawFormat: "NV12", codec: VideoCodecH264}
	args := withVARotation(buildVAWaylandVideoPipeline(3, 42, 30, encoder, 1920, 1080, true), "90l")
	pipeline := strings.Join(args, " ")
	if !strings.Contains(pipeline, "vapostproc video-direction=90l") {
		t.Fatal(pipeline)
	}
	for _, bad := range []string{"videoconvert", "videoflip", "compositor", "always-copy=true"} {
		if strings.Contains(pipeline, bad) {
			t.Fatalf("CPU stage %s in %s", bad, pipeline)
		}
	}
}
