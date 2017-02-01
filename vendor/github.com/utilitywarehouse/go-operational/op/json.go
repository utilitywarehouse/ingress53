// +build go1.7

package op

import (
	"encoding/json"
	"io"
)

func newEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetIndent("  ", "  ")
	return enc
}
