package airplay

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
)

// reconnectForTimingRetry retains only the pairing identity and user settings.
// Nonces, stream keys, Digest challenges and FairPlay state belong to the old
// connection. The failed SETUP attempt has already closed its media sockets.
func (c *AirPlayClient) reconnectForTimingRetry(ctx context.Context) (err error) {
	_ = c.Close()
	if c.PairKeys == nil || len(c.PairKeys.Ed25519Private) != ed25519.PrivateKeySize {
		return fmt.Errorf("no pairing identity available for authenticated retry")
	}
	c.mu.Lock()
	c.PairKeys = &PairKeys{
		Ed25519Public:  c.PairKeys.Ed25519Public,
		Ed25519Private: c.PairKeys.Ed25519Private,
	}
	c.conn = nil
	c.cseq.Store(0)
	c.sessionID = generateUUID()
	c.encrypted = false
	c.encWriteKey, c.encReadKey = nil, nil
	c.encWriteNonce, c.encReadNonce = 0, 0
	c.encCipher = nil
	c.fpKey, c.fpIV, c.FpEkey, c.fpM3, c.fpAesKey = nil, nil, nil, nil, nil
	c.streamKey, c.streamIV = nil, nil
	c.authChallenge = nil
	c.mu.Unlock()
	defer func() {
		if err != nil {
			_ = c.Close()
		}
	}()
	if err = c.Connect(ctx); err != nil {
		return err
	}
	if _, err = c.GetInfo(); err != nil {
		return err
	}
	if c.transientPairing {
		// The receiver may discard transient identities when TCP closes. Repeat
		// the already-authorized PIN-less exchange, using the same identity.
		err = c.performTransientSetupAndVerify(ctx)
	} else {
		err = c.PairVerify(ctx)
	}
	if err != nil {
		return fmt.Errorf("pair-verify: %w", err)
	}
	if !c.encrypted {
		return fmt.Errorf("NTP retry did not restore encrypted control")
	}
	if err = c.FairPlaySetup(ctx); err != nil && !errors.Is(err, ErrFairPlayUnsupported) {
		return fmt.Errorf("FairPlay setup: %w", err)
	}
	return nil
}
