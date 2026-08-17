package main

import (
	"encoding/json"
	"os"
)

// newJSONEncoder returns a writer for the -print-config output.
func newJSONEncoder() func(any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode
}
