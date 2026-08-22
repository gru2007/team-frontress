package war

import "testing"

func TestProgressPercentIncludesPartialPush(t *testing.T) {
	cases := []struct {
		stage, stages int
		push          float64
		status        FrontStatus
		want          int
	}{
		{stage: 0, stages: 1, push: 0.5, status: FrontActive, want: 50},
		{stage: 1, stages: 3, push: 0.5, status: FrontActive, want: 50},
		{stage: 3, stages: 3, status: FrontWon, want: 100},
		{stage: 0, stages: 3, status: FrontCollapsed, want: 0},
	}
	for _, tc := range cases {
		if got := ProgressPercent(tc.stage, tc.push, tc.stages, tc.status); got != tc.want {
			t.Errorf("ProgressPercent(%d, %.2f, %d, %s) = %d, want %d",
				tc.stage, tc.push, tc.stages, tc.status, got, tc.want)
		}
	}
}
