package jsonkeys_test

import (
	"os"
	"testing"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
	"github.com/gru2007/team-frontress/services/gc/internal/jsonkeys"
	"github.com/gru2007/team-frontress/services/gc/internal/war"
)

// The example files document every setting; each one must be one the GC reads.
func TestExamplesHaveNoUnknownKeys(t *testing.T) {
	for path, v := range map[string]any{
		"../../gc.example.json":      config.Config{},
		"../../theater.example.json": war.Theater{},
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if keys := jsonkeys.Unknown(raw, v); len(keys) > 0 {
			t.Errorf("%s: settings the GC does not read: %v", path, keys)
		}
	}
}
