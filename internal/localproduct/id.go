package localproduct

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

type randomIDs struct{}

func (randomIDs) NewID(kind string) (string, error) {
	if !canonical(kind) || strings.Contains(kind, "/") {
		return "", fmt.Errorf("id kind is invalid")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate %s id: %w", kind, err)
	}
	return kind + "-" + hex.EncodeToString(random), nil
}
