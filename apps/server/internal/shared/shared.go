package shared

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

func Now() time.Time {
	return time.Now().UTC()
}

func NewID(prefix string) string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return prefix + "_" + hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}

func CompactJSON(value any, max int) string {
	bytes, err := json.Marshal(value)
	if err != nil {
		return TrimRunes(fmt.Sprint(value), max)
	}
	return TrimRunes(string(bytes), max)
}

func TrimRunes(value string, max int) string {
	runes := []rune(value)
	if max <= 0 || len(runes) <= max {
		return value
	}
	return string(runes[:max-1]) + "..."
}
