package httpapi

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// hexOrBase64 decodes a Steam auth ticket as the client happened to encode it.
// Both encodings are in use in the wild — the C++ side has a hex helper, web
// tooling reaches for base64 — and guessing wrong would reject a legitimate
// player, so both are accepted.
func hexOrBase64(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if b, err := hex.DecodeString(raw); err == nil {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil {
		return b, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(raw); err == nil {
		return b, nil
	}
	return nil, fmt.Errorf("ticket is neither hex nor base64")
}
