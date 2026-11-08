package demoauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"rzdarctica.local/backend/internal/core/jsonutil"
)

// strictJSON combines shared duplicate-key/UTF-8 validation with the closed target schema.
func strictJSON(data []byte, dst any) error {
	if !jsonutil.UniqueKeys(data) {
		return errors.New("ambiguous JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}

func exactFields(data []byte, names ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	return true
}
