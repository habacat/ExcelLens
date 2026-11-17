package workbook

import (
	"context"
	"testing"
)

func TestAxisVisibilityMetadataPagesAndNormalizedSettingsAreIndependent(t *testing.T) {
	depth := uint8(2)
	input := AxisViewSettings{Depth: &depth, Ranges: []AxisVisibilityOverride{{Min: 7, Max: 7, Hidden: true}, {Min: 5, Max: 6, Hidden: true}}}
	normal, err := input.Normalized(100)
	if err != nil || normal.Mode != "original" || len(normal.Ranges) != 1 || normal.Ranges[0].Min != 5 || normal.Ranges[0].Max != 7 {
		t.Fatal(normal, err)
	}
	*normal.Depth = 1
	normal.Ranges[0].Min = 1
	if depth != 2 || len(input.Ranges) != 2 || input.Ranges[0].Min != 7 {
		t.Fatal("normalization changed caller settings")
	}
	layout := SourceLayout{RowStates: []SourceAxisRange{{Min: 2, Max: 3, Level: 1}, {Min: 4, Max: 4, Level: 2}, {Min: 5, Max: 6, Level: 1}}}
	axis, err := layout.BuildAxisVisibility(context.Background(), "row", 100, AxisViewSettings{})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := axis.GroupPage(1, 1)
	if err != nil || axis.GroupCount() != 2 || len(groups) != 1 || groups[0].Min != 4 || groups[0].Parent != 1 {
		t.Fatal(groups, err)
	}
	groups[0].Min = 99
	again, _ := axis.GroupPage(1, 1)
	if again[0].Min != 4 {
		t.Fatal("page mutated immutable source outline")
	}
	end, err := axis.GroupPage(2, 10)
	if err != nil || len(end) != 0 {
		t.Fatal("end page", end, err)
	}
	for _, request := range [][2]int{{-1, 1}, {3, 1}, {0, -1}, {0, 10001}} {
		if _, err := axis.GroupPage(request[0], request[1]); err == nil {
			t.Fatal("invalid metadata page accepted", request)
		}
	}
}
