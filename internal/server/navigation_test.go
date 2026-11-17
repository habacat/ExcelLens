package server

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"testing"
)

func TestBoundaryJumpContiguousAndSparse(t *testing.T) {
	data := []int{2, 3, 8, 11}
	cases := []struct{ start, dir, want int }{
		{2, 1, 3}, {3, 1, 8}, {5, 1, 8}, {8, 1, 11}, {11, 1, 20},
		{11, -1, 8}, {8, -1, 3}, {3, -1, 2}, {2, -1, 1}, {5, -1, 3}, {1, 1, 2},
	}
	for _, c := range cases {
		if got := boundaryJump(data, c.start, c.dir, 1, 20); got != c.want {
			t.Errorf("start%d dir%d got%d expected%d", c.start, c.dir, got, c.want)
		}
	}
}
func TestNavigateAPIUsesTrueContentOnly(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "tiny.xlsx")
	writeMultiSheetWorkbook(t, file)
	a := prepareFeatureApp(t, file)
	defer a.Close()
	test := func(ref, direction, want string) {
		endpoint := "/api/navigate?sheet=First&ref=" + url.QueryEscape(ref) + "&direction=" + direction
		body := featureCall(t, a, "GET", endpoint, "").Body.Bytes()
		var got struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Ref != want {
			t.Errorf("%s %s: got %s want %s", ref, direction, got.Ref, want)
		}
	}
	test("A1", "right", "B1")
	test("B1", "left", "A1")
	test("A1", "down", "A1")
}
