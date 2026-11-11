package jsonkeys

import (
	"reflect"
	"testing"
)

type inner struct {
	Map  string `json:"map"`
	Size int    `json:"min_players,omitempty"`
}

type outer struct {
	Name   string             `json:"name"`
	Fields map[string][]inner `json:"battlefields"`
	Nodes  []inner            `json:"nodes"`
	Skip   string             `json:"-"`
}

func TestUnknown(t *testing.T) {
	raw := []byte(`{
		"_comment": "ignored",
		"//note": "ignored",
		"name": "x",
		"bonus_xp": 50,
		"battlefields": {"arena": [{"map": "arena_well", "min_player": 2}]},
		"nodes": [{"map": "a"}, {"MAP": "b", "xp": 1}]
	}`)
	got := Unknown(raw, outer{})
	want := []string{"battlefields.arena[0].min_player", "bonus_xp", "nodes[1].xp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
