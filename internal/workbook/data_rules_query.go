package workbook

import (
	"context"
	"errors"
	"slices"
	"unicode/utf8"

	"excellens/internal/formats"
)

// BudgetBytes is conservative retained-metadata accounting, not measured
// process memory. Callers retaining snapshots must also enforce their limits.
func (index *SourceDataRuleIndex) BudgetBytes() int64 { return index.bytes }

type DataRuleQuery struct {
	Row, Col, Offset, Count int
	Type, Status            string
}

type DataRuleListItem struct {
	ID         int    `json:"id"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	SqRef      string `json:"sqref"`
	RangeCount int    `json:"rangeCount"`
	ListItems  int    `json:"listItems"`
	ListSource string `json:"listSource,omitempty"`
	Shortened  bool   `json:"shortened"`
}

type DataRuleListPage struct {
	Items         []DataRuleListItem `json:"items"`
	Offset        int                `json:"offset"`
	Previous      int                `json:"previous"`
	Next          int                `json:"next"`
	Matched       int                `json:"matched"`
	Total         int                `json:"total"`
	ScopeComplete bool               `json:"scopeComplete"`
}

// List never copies full definitions, range arrays or list candidates. A cell
// query reuses the same compressed range tree as At, independent of view order.
func (index *SourceDataRuleIndex) List(ctx context.Context, query DataRuleQuery) (DataRuleListPage, error) {
	if query.Offset < 0 || query.Count < 1 || query.Count > formats.DataRulePageRows || query.Type != "" && !slices.Contains(dataRuleTypes, query.Type) || query.Status != "" && query.Status != "described" && query.Status != "issues" {
		return DataRuleListPage{}, errors.New("数据规则筛选或分页参数无效")
	}
	if query.Row < 0 || query.Col < 0 || (query.Row == 0) != (query.Col == 0) || query.Row > index.maxRow || query.Col > index.maxCol {
		return DataRuleListPage{}, errors.New("数据规则查询超出源格式坐标范围")
	}
	var ids []int
	if query.Row > 0 {
		var err error
		ids, err = index.idsAt(ctx, query.Row, query.Col)
		if err != nil {
			return DataRuleListPage{}, err
		}
	}
	out := DataRuleListPage{Items: make([]DataRuleListItem, 0, query.Count), Offset: query.Offset, Previous: max(0, query.Offset-query.Count), Total: len(index.rules), ScopeComplete: index.summary.Complete && index.summary.UnknownScopes == 0}
	visit := func(rule SourceDataRule) {
		if query.Type != "" && rule.Type != query.Type || query.Status == "described" && rule.Status != "described" || query.Status == "issues" && rule.Status == "described" {
			return
		}
		if out.Matched >= query.Offset && len(out.Items) < query.Count {
			text, shortened := rule.SqRef, false
			if utf8.RuneCountInString(text) > formats.DataRuleLabelRunes {
				text, shortened = string([]rune(text)[:formats.DataRuleLabelRunes])+"…", true
			}
			out.Items = append(out.Items, DataRuleListItem{ID: rule.ID, Type: rule.Type, Status: rule.Status, SqRef: text, RangeCount: len(rule.Ranges), ListItems: len(rule.InlineList), ListSource: rule.ListSource, Shortened: shortened})
		}
		out.Matched++
	}
	if query.Row > 0 {
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return DataRuleListPage{}, err
			}
			visit(index.rules[id])
		}
	} else {
		for _, rule := range index.rules {
			if err := ctx.Err(); err != nil {
				return DataRuleListPage{}, err
			}
			visit(rule)
		}
	}
	if err := ctx.Err(); err != nil {
		return DataRuleListPage{}, err
	}
	if query.Offset > out.Matched {
		return DataRuleListPage{}, errors.New("数据规则分页位置超出当前结果，请从第一页重试")
	}
	out.Next = query.Offset + len(out.Items)
	return out, nil
}

type DataRuleDetailPage struct {
	Item          SourceDataRule `json:"item"`
	RangeCount    int            `json:"rangeCount"`
	RangeOffset   int            `json:"rangeOffset"`
	RangePrevious int            `json:"rangePrevious"`
	RangeNext     int            `json:"rangeNext"`
	ListCount     int            `json:"listCount"`
	ListOffset    int            `json:"listOffset"`
	ListPrevious  int            `json:"listPrevious"`
	ListNext      int            `json:"listNext"`
}

// Detail retains exact source definitions while paging arrays independently.
// A caller can never mutate the shared snapshot, even through nested slices.
func (index *SourceDataRuleIndex) Detail(id, rangeOffset, listOffset, count int) (DataRuleDetailPage, error) {
	if id < 1 || id > len(index.rules) || count < 1 || count > formats.DataRulePageRows {
		return DataRuleDetailPage{}, errors.New("数据规则条目或分页参数无效")
	}
	rule := index.rules[id-1]
	if rangeOffset < 0 || rangeOffset > len(rule.Ranges) || listOffset < 0 || listOffset > len(rule.InlineList) {
		return DataRuleDetailPage{}, errors.New("数据规则详情分页位置无效")
	}
	out := DataRuleDetailPage{RangeCount: len(rule.Ranges), RangeOffset: rangeOffset, RangePrevious: max(0, rangeOffset-count), RangeNext: min(len(rule.Ranges), rangeOffset+count), ListCount: len(rule.InlineList), ListOffset: listOffset, ListPrevious: max(0, listOffset-count), ListNext: min(len(rule.InlineList), listOffset+count)}
	rule.Ranges = slices.Clone(rule.Ranges[rangeOffset:out.RangeNext])
	rule.InlineList = slices.Clone(rule.InlineList[listOffset:out.ListNext])
	rule.ContinuationHex = slices.Clone(rule.ContinuationHex)
	rule.Attributes = cloneDataRuleAttributes(rule.Attributes)
	out.Item = rule
	return out, nil
}

// Area returns one exact saved rectangle without copying a full definition.
func (index *SourceDataRuleIndex) Area(id, area int) (CellMerge, error) {
	if id < 1 || id > len(index.rules) || area < 0 || area >= len(index.rules[id-1].Ranges) {
		return CellMerge{}, errors.New("请选择可确认的数据规则源区域")
	}
	return index.rules[id-1].Ranges[area], nil
}
