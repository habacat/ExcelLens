package editfile

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"

	"excellens/internal/formats"
)

// A workbook window owns its active tab; each worksheet stores that window's
// selection separately. Both records must agree after changing the tab list.
func (a *sheetCollectionEdit) prepareViews(ctx context.Context, raw []byte) error {
	var states []string
	var current []int
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		t, ok := e.token.(xml.StartElement)
		if !ok {
			return nil
		}
		if e.parent() == "sheets" && e.local() == "sheet" {
			states = append(states, attribute(t, "", "state"))
		}
		if e.parent() == "bookViews" && e.local() == "workbookView" {
			v, _ := strconv.Atoi(attribute(t, "", "activeTab"))
			current = append(current, v)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(states) != len(a.p.layout.Sheets) {
		return ErrXLSX
	}
	a.position = a.index + 1
	a.copyState = ""
	if a.op.Kind == "copySheet" {
		a.copyState = states[a.index]
		for a.position < len(states) && states[a.position] != "" && states[a.position] != "visible" {
			a.position++
		}
	}
	drop := a.op.Kind == "deleteSheet"
	if drop {
		a.position = a.index
		states = slices.Delete(states, a.index, a.index+1)
	} else {
		states = slices.Insert(states, a.position, a.copyState)
	}
	if len(current) == 0 {
		current = []int{0}
		a.addView = true
	}
	visible := func(i int) bool { return states[i] == "" || states[i] == "visible" }
	closest := func(i int) int {
		for j := max(0, i); j < len(states); j++ {
			if visible(j) {
				return j
			}
		}
		for j := min(i-1, len(states)-1); j >= 0; j-- {
			if visible(j) {
				return j
			}
		}
		return -1
	}
	for _, old := range current {
		active := a.position
		if drop {
			active = old
			if active > a.index {
				active--
			}
			if old == a.index || active < 0 || active >= len(states) || !visible(active) {
				active = closest(active)
			}
		} else if !visible(active) {
			active = closest(active)
		}
		if active < 0 {
			return fmt.Errorf("工作簿至少需要保留一张可见工作表")
		}
		a.activeViews = append(a.activeViews, active)
	}
	return nil
}

func (a *sheetCollectionEdit) selectViews(ctx context.Context) error {
	parts := make([]string, 0, len(a.p.layout.Sheets)+1)
	for i, s := range a.p.layout.Sheets {
		if a.op.Kind == "deleteSheet" && i == a.index {
			continue
		}
		parts = append(parts, s.Part)
	}
	if a.op.Kind != "deleteSheet" {
		parts = slices.Insert(parts, a.position, a.newPart)
	}
	for i, part := range parts {
		if err := a.selectSheetView(ctx, part, i); err != nil {
			return err
		}
	}
	return nil
}

func (a *sheetCollectionEdit) selectSheetView(ctx context.Context, part string, index int) error {
	data, revised := a.updates[part]
	if !revised {
		data, revised = a.p.revisedParts[part]
	}
	var source io.Reader
	if revised {
		source = bytes.NewReader(data)
	} else {
		f := a.p.files[part]
		if f == nil {
			return ErrXLSX
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		defer r.Close()
		source = r
	}
	ps, err := sheetSelectionPatches(ctx, source, index, a.activeViews)
	if err != nil || len(ps) == 0 {
		return err
	}
	if revised {
		a.updates[part], err = applyXML(ctx, data, ps)
	} else {
		if a.viewPatches == nil {
			a.viewPatches = make(map[string][]xmlPatch)
		}
		sortPatches(ps)
		a.viewPatches[part] = ps
	}
	return err
}

// SpreadsheetML places sheetViews before the column and cell records. Read
// just this prefix, then stream any necessary splices while writing the ZIP.
func sheetSelectionPatches(ctx context.Context, source io.Reader, index int, active []int) ([]xmlPatch, error) {
	var prefix bytes.Buffer
	r := bufio.NewReader(io.TeeReader(io.LimitReader(source, formats.EditXLSXPartBytes), &prefix))
	var ps []xmlPatch
	present := map[int]bool{}
	var group xmlEvent
	var groupEnd xmlEvent
	insert := -1
	done := errors.New("worksheet views complete")
	err := walkXMLBuffered(ctx, r, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if t.Name.Space != mainNS {
				return nil
			}
			if len(e.stack) == 2 && insert < 0 && e.local() != "sheetPr" && e.local() != "dimension" {
				insert = e.from
				if e.local() != "sheetViews" {
					return done
				}
			}
			if len(e.stack) == 2 && e.local() == "sheetViews" {
				group = e
			}
			if e.parent() != "sheetViews" || e.local() != "sheetView" {
				return nil
			}
			id, _ := strconv.Atoi(attribute(t, "", "workbookViewId"))
			if id < 0 || id >= len(active) {
				return nil
			}
			present[id] = true
			current := isTrue(attribute(t, "", "tabSelected"))
			if current != (active[id] == index) {
				val := "0"
				if !current {
					val = "1"
				}
				ap, err := setXMLAttribute(prefix.Bytes(), e.from, e.to, "tabSelected", val)
				if err != nil {
					return err
				}
				ps = append(ps, ap...)
			}
		case xml.EndElement:
			if len(e.stack) == 2 && t.Name == (xml.Name{Space: mainNS, Local: "sheetViews"}) {
				groupEnd = e
				return done
			}
			if len(e.stack) == 1 && insert < 0 {
				insert = e.from
			}
		}
		return nil
	})
	if err != nil && err != done {
		return nil, err
	}
	var nodes string
	for id, v := range active {
		if v == index && !present[id] {
			nodes += fmt.Sprintf(`<sheetView xmlns="%s" workbookViewId="%d" tabSelected="1"/>`, mainNS, id)
		}
	}
	if nodes != "" {
		if group.to == 0 {
			ps = append(ps, xmlPatch{insert, insert, `<sheetViews xmlns="` + mainNS + `">` + nodes + "</sheetViews>"})
		} else if group.to == groupEnd.to {
			ps = append(ps, xmlPatch{group.to - 2, group.to, ">" + nodes + closingTag(prefix.Bytes()[group.from:group.to])})
		} else {
			ps = append(ps, xmlPatch{groupEnd.from, groupEnd.from, nodes})
		}
	}
	return ps, nil
}
