package jsonutil

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// SHA256 hashes compact JSON with sorted object keys and exact JSON numbers.
func SHA256(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var decoded any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err = d.Decode(&decoded); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	if err = e.Encode(decoded); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:]), nil
}
