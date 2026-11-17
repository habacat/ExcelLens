package searchdsl

import (
	"strings"
	"testing"
)

func TestQueryRealWorldExpressions(t *testing.T) {
	schema := NewSchema(map[int]string{1: "机构", 2: "品种", 3: "账面价值", 4: "状态", 5: "日期", 6: "备注", 7: "空列"}, 7)
	values := map[int]Cell{
		1: {Column: 1, Display: "北京分公司", Raw: "北京分公司"},
		2: {Column: 2, Display: "债券", Raw: "债券"},
		3: {Column: 3, Display: "9,007,199,254,740,993.00", Raw: "9007199254740993", Numeric: true},
		4: {Column: 4, Display: "正常"},
		5: {Column: 5, Display: "2026-01-05", Raw: "2026-01-05"},
		6: {Column: 6, Display: "正常", Raw: "正常", Formula: "SUMIF(A:A,B:B,C:C)"},
	}
	row := Row{Sheet: "Invest", Cells: values}
	cases := []struct {
		query string
		want  bool
	}{
		{`[机构]:"北京" AND [账面价值]>9007199254740992`, true},
		{`[机构]:"北京" AND [账面价值]>9007199254740993`, false},
		{`[账面价值]>=9007199254740993 AND [状态]:正常`, true},
		{`(B IN ("基金","债券") OR A:上海) AND NOT D:作废`, true},
		{`[账面价值] BETWEEN 9007199254740992 AND 9007199254740994`, true},
		{`[账面价值] BETWEEN 9007199254740994 AND 9007199254740995`, false},
		{`E>=2026-01-01 AND E<2026-02-01`, true},
		{`sheet:Invest AND [空列] IS EMPTY`, true},
		{`sheet:Other OR [空列] IS NOT EMPTY`, false},
		{`A~"^北京" AND -作废`, true},
		{`formula:SUMIF`, true},
		{`B="债券"`, true},
		{`B!="债券"`, false},
		{`A:北京 B:债券`, true},
		{`[机构]:上海 OR [品种]:债券`, true},
		{`C<=-10`, false},
		{`[账面价值]=9007199254740992`, false},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			q, err := Parse(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if err = q.CheckSchema(schema); err != nil {
				t.Fatal(err)
			}
			if got := q.Matches(row, schema); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
func TestSearchDSLRejectsBadInput(t *testing.T) {
	bad := []string{``, `[机构`, `B>`, `A~"["`, `A IN ()`, `A BETWEEN 10 20`, `([机构]:北京`, `A IS nonsense`, strings.Repeat("x", MaxQueryLength+1)}
	for _, raw := range bad {
		t.Run(raw[:min(len(raw), 32)], func(t *testing.T) {
			if _, err := Parse(raw); err == nil {
				t.Fatalf("expected parser rejection: %q", raw)
			}
		})
	}
	schema := NewSchema(map[int]string{1: "机构", 2: "重复", 3: "重复"}, 3)
	for _, raw := range []string{`[缺少列]:北京`, `[重复]:值`} {
		q, err := Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if q.CheckSchema(schema) == nil {
			t.Fatalf("should reject unknown or ambiguous header: %s", raw)
		}
	}
}
