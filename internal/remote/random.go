package remote

import (
	"crypto/rand"
	"encoding/hex"
)

func randomSuffix() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failures are exceptionally rare; fall back to a value
		// that still avoids predictable collisions in tests.
		return "0000000000000000"
	}
	return hex.EncodeToString(buf[:])
}
