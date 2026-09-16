package airplay

import (
	"errors"
	"strings"
	"testing"
)

func TestMissingPTPClockIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		response map[string]interface{}
		want     bool
	}{
		{"omitted peer", map[string]interface{}{}, true},
		{"LG peer without clock", map[string]interface{}{"timingPeerInfo": map[string]interface{}{"ID": "peer", "Addresses": []interface{}{"192.0.2.1"}}}, true},
		{"empty peer", map[string]interface{}{"timingPeerInfo": map[string]interface{}{}}, true},
		{"null peer", map[string]interface{}{"timingPeerInfo": nil}, false},
		{"malformed peer", map[string]interface{}{"timingPeerInfo": "invalid"}, false},
		{"zero clock", map[string]interface{}{"timingPeerInfo": map[string]interface{}{"ClockID": uint64(0)}}, false},
		{"malformed clock", map[string]interface{}{"timingPeerInfo": map[string]interface{}{"ClockID": "invalid"}}, false},
		{"valid clock", map[string]interface{}{"timingPeerInfo": map[string]interface{}{"ClockID": uint64(42)}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := missingPTPClockIdentity(test.response); got != test.want {
				t.Fatalf("retry eligible=%v, want %v", got, test.want)
			}
		})
	}
}

func TestNTPRetryPreservesPINIdentityAndDigest(t *testing.T) {
	server, client, ctx := newReceiverServerTestPair(t, ReceiverConfig{Profile: ReceiverProfileLG, Auth: ReceiverAuthCombined, Code: "test password"})
	if err := client.Pair(ctx, "test password"); err != nil {
		t.Fatal(err)
	}
	pairingID := client.PairingID
	oldSessionID := client.sessionID
	oldConnection := client.conn
	before := server.Stats()
	prepared := 0
	session, err := client.SetupMirrorWithVideoPreparation(ctx, StreamConfig{NoAudio: true}, func(int, int) error { prepared++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.timingProtocol != timingProtocolNTP || !client.encrypted {
		t.Fatal("retry did not negotiate encrypted NTP")
	}
	if client.PairingID != pairingID || client.sessionID == oldSessionID || client.conn == oldConnection {
		t.Fatal("retry did not preserve identity on a fresh session")
	}
	if prepared != 1 {
		t.Fatalf("capture preparations=%d, want 1", prepared)
	}
	stats := server.Stats()
	if stats.Connections != 2 || stats.PairSetup != before.PairSetup || stats.PairVerify != before.PairVerify+2 {
		t.Fatalf("unexpected reauthentication: %+v, before %+v", stats, before)
	}
	if stats.DigestChallenges != 2 {
		t.Fatalf("Digest challenge was not refreshed: %+v", stats)
	}
	if stats.TeardownRequests != 1 {
		t.Fatalf("partial session not torn down: %+v", stats)
	}
}

func TestNTPRetryStopsAfterNTPRejection(t *testing.T) {
	server, client, ctx := newReceiverServerTestPair(t, ReceiverConfig{Profile: ReceiverProfileModern}, func(s *ReceiverServer) { s.profile.providePTPClockIdentity = false })
	if err := client.Pair(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := client.FairPlaySetup(ctx); err != nil {
		t.Fatal(err)
	}
	session, err := client.SetupMirror(ctx, StreamConfig{NoAudio: true})
	if session != nil || !errors.Is(err, errMissingPTPPeer) || !strings.Contains(err.Error(), "NTP retry failed") {
		t.Fatalf("expected both failures, got session=%v err=%v", session, err)
	}
	stats := server.Stats()
	if stats.Connections != 2 || stats.SetupRequests != 4 || stats.TeardownRequests != 1 {
		t.Fatalf("retry was not bounded/cleaned: %+v", stats)
	}
	if stats.FairPlayRequests != 4 {
		t.Fatalf("FairPlay was not renegotiated: %+v", stats)
	}
}

func TestNTPRetryAfterMediaFirstSetup(t *testing.T) {
	server, client, ctx := newReceiverServerTestPair(t, ReceiverConfig{Profile: ReceiverProfileLG}, func(s *ReceiverServer) { s.profile.setupOrder = receiverSetupMediaFirst })
	if err := client.Pair(ctx, ""); err != nil {
		t.Fatal(err)
	}
	prepared := 0
	session, err := client.SetupMirrorWithVideoPreparation(ctx, StreamConfig{NoAudio: true}, func(int, int) error { prepared++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	stats := server.Stats()
	if session.timingProtocol != timingProtocolNTP || stats.Connections != 2 || stats.SetupRequests != 5 || stats.TeardownRequests != 1 || prepared != 1 {
		t.Fatalf("unexpected media-first retry: prepared=%d stats=%+v", prepared, stats)
	}
}
