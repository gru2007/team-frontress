package maps

import "testing"

func TestSelectedFromBitsUsesValveMasterMapIndices(t *testing.T) {
	if len(All) < 34 {
		t.Fatal("stock map table is unexpectedly short")
	}
	got := SelectedFromBits([]uint32{1 << 0, 1 << 1})
	want := []string{All[0].Name, All[33].Name}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("SelectedFromBits() = %v, want %v", got, want)
	}
}

func TestSelectedFromBitsEmptyMeansNoPreference(t *testing.T) {
	if got := SelectedFromBits(nil); len(got) != 0 {
		t.Fatalf("SelectedFromBits(nil) = %v", got)
	}
}
