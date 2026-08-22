package war

import "testing"

func TestIndustrialTheaterHasStrategicScaleAndLiveContactLine(t *testing.T) {
	theater, err := LoadTheater("../../theater.industrial.json")
	if err != nil {
		t.Fatalf("LoadTheater: %v", err)
	}
	if got := len(theater.Nodes); got < 50 || got > 100 {
		t.Fatalf("industrial theater has %d nodes; want 50..100", got)
	}

	op := theater.OpeningFor(1)
	contested := 0
	for _, node := range theater.Nodes {
		if op.Owners[node.ID] != SideNone {
			continue
		}
		hasRed, hasBlu := false, false
		for _, id := range node.Adjacent {
			switch op.Owners[id] {
			case SideRed:
				hasRed = true
			case SideBlu:
				hasBlu = true
			}
		}
		if hasRed && hasBlu {
			contested++
		}
	}
	if contested < 8 {
		t.Fatalf("opening has only %d neutral contact sectors; want at least 8", contested)
	}
}
