package war

import "testing"

// The shipped example is documentation people copy. If it stops loading, the
// first person to find out should not be an operator.
func TestExampleTheaterLoads(t *testing.T) {
	th, err := LoadTheater("../../theater.example.json")
	if err != nil {
		t.Fatalf("theater.example.json: %v", err)
	}
	if len(th.Battlefields) == 0 {
		t.Fatal("the example has no battlefield pools")
	}
	for _, n := range th.Nodes {
		for i, s := range n.Plan {
			if len(th.FieldsFor(s)) == 0 {
				t.Fatalf("node %s plan[%d] (%q) has nowhere to be fought", n.ID, i, s.Kind)
			}
		}
	}
}
