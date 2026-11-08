package jsonutil

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// UniqueKeys rejects repeated fields at any depth as well as malformed JSON.
func UniqueKeys(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var walk func() bool
	walk = func() bool {
		value, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := value.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] || !walk() {
					return false
				}
				seen[name] = true
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !walk() {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		}
		return false
	}
	if !walk() {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
