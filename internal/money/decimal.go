package money

import (
	"bytes"
	"encoding/json"
)

// DecimalInput accepts either a JSON string ("120.50") or a JSON number
// (120.50) and keeps the exact literal text, so no float parsing ever happens.
// The Parse* functions validate the text.
type DecimalInput string

func (d *DecimalInput) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*d = DecimalInput(s)
		return nil
	}
	if bytes.Equal(b, []byte("null")) {
		*d = ""
		return nil
	}
	// Raw number literal: keep the text verbatim; Parse* validates it.
	*d = DecimalInput(b)
	return nil
}
