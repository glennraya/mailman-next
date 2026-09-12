package webhook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// hmacHex is the signature both signing schemes reduce to: the hex-encoded
// HMAC-SHA256 of some bytes under the route's key.
func hmacHex(key string, message []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(message)
	return hex.EncodeToString(mac.Sum(nil))
}

// newToken mints the 50-character random token Mailgun pairs with a
// timestamp. Length and alphabet match what Mailgun documents, since a
// handler may validate them before it verifies the signature.
func newToken() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

	buf := make([]byte, 50)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate webhook token: %w", err)
	}
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(buf), nil
}

// newUUID is a version 4 UUID, for the provider-assigned identifiers a
// payload has to carry. Eight lines of crypto/rand rather than a dependency
// for one call.
func newUUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}

	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 10

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}
