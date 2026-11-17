// Package printing computes bounded, source-bound print pages. It does not
// read cell values, write workbooks, contact printers or render a desktop UI.
package printing

import (
	"context"
	"errors"
	"maps"
	"slices"

	"excellens/internal/workbook"
)

type Interval struct {
	First int `json:"first"`
	Last  int `json:"last"`
}
type Paper struct {
	Width  string `json:"width"`
	Height string `json:"height"`
}
type Margins struct {
	Left   string `json:"left"`
	Right  string `json:"right"`
	Top    string `json:"top"`
	Bottom string `json:"bottom"`
	Header string `json:"header"`
	Footer string `json:"footer"`
}
type Fit struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}
type Options struct {
	UsePreviewDefaults bool                 `json:"usePreviewDefaults"`
	Paper              *Paper               `json:"paper,omitempty"`
	Orientation        string               `json:"orientation,omitempty"`
	Margins            *Margins             `json:"margins,omitempty"`
	ScalePercent       string               `json:"scalePercent,omitempty"`
	Fit                *Fit                 `json:"fit,omitempty"`
	Areas              []workbook.CellMerge `json:"areas"`
	RepeatRows         []Interval           `json:"repeatRows"`
	RepeatColumns      []Interval           `json:"repeatColumns"`
	MissingRowHeight   string               `json:"missingRowHeight,omitempty"`
	MissingColumnWidth string               `json:"missingColumnWidth,omitempty"`
	PageOrder          string               `json:"pageOrder,omitempty"`
	FirstPageNumber    *int64               `json:"firstPageNumber,omitempty"`
	IgnoreManualBreaks bool                 `json:"ignoreManualBreaks"`
}

// Every dimension is integer nanopts; strings preserve transport precision.
type Dimensions struct {
	Width  int64 `json:"width,string"`
	Height int64 `json:"height,string"`
}
type MarginUnits struct {
	Left   int64 `json:"left,string"`
	Right  int64 `json:"right,string"`
	Top    int64 `json:"top,string"`
	Bottom int64 `json:"bottom,string"`
	Header int64 `json:"header,string"`
	Footer int64 `json:"footer,string"`
}
type Scale struct {
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
	Percent     string `json:"percent"`
}
type Part struct {
	First    int   `json:"first"`
	Last     int   `json:"last"`
	Offset   int64 `json:"offset,string"`
	Length   int64 `json:"length,string"`
	Repeated bool  `json:"repeated"`
}
type Page struct {
	Number   int64      `json:"number"`
	Area     int        `json:"area"`
	Rows     []Part     `json:"rows"`
	Columns  []Part     `json:"columns"`
	Body     Dimensions `json:"body"`
	Content  Dimensions `json:"content"`
	OffsetX  int64      `json:"offsetX,string"`
	OffsetY  int64      `json:"offsetY,string"`
	Oversize bool       `json:"oversize"`
}
type Summary struct {
	SourceID        string                         `json:"sourceId"`
	Sheet           string                         `json:"sheet"`
	Pages           int                            `json:"pages"`
	Paper           Dimensions                     `json:"paper"`
	Margins         MarginUnits                    `json:"margins"`
	Scale           Scale                          `json:"scale"`
	Areas           []workbook.CellMerge           `json:"areas"`
	RepeatRows      []Interval                     `json:"repeatRows"`
	RepeatColumns   []Interval                     `json:"repeatColumns"`
	PageOrder       string                         `json:"pageOrder"`
	FirstPageNumber int64                          `json:"firstPageNumber"`
	Headings        Dimensions                     `json:"headings"`
	Defaults        workbook.PrintGeometryDefaults `json:"defaults"`
	Approximate     bool                           `json:"approximate"`
	Origins         map[string]string              `json:"origins"`
	Notices         []string                       `json:"notices"`
}
type Plan struct {
	source   *workbook.PrintSource
	summary  Summary
	pages    []Page
	geometry *workbook.PrintGeometryView
}

func (plan *Plan) Summary() Summary {
	result := plan.summary
	result.Origins = maps.Clone(result.Origins)
	result.Notices = slices.Clone(result.Notices)
	result.Areas = slices.Clone(result.Areas)
	result.RepeatRows = slices.Clone(result.RepeatRows)
	result.RepeatColumns = slices.Clone(result.RepeatColumns)
	if result.Defaults.Row != nil {
		value := *result.Defaults.Row
		result.Defaults.Row = &value
	}
	if result.Defaults.Column != nil {
		value := *result.Defaults.Column
		result.Defaults.Column = &value
	}
	return result
}
func (plan *Plan) Validate(ctx context.Context) error { return plan.source.Validate(ctx) }

// A conservative retention budget includes source metadata, axis storage and
// slice capacity, rather than just the small encoded summary sent to a client.
func (plan *Plan) BudgetBytes() int64 {
	bytes := plan.source.BudgetBytes() + 4096 + int64(cap(plan.pages))*256
	for _, page := range plan.pages {
		bytes += int64(cap(page.Rows)+cap(page.Columns)) * 64
	}
	for key, value := range plan.summary.Origins {
		bytes += int64(len(key)+len(value)) + 128
	}
	for _, notice := range plan.summary.Notices {
		bytes += int64(len(notice)) + 32
	}
	return bytes + int64(cap(plan.summary.Areas)+cap(plan.summary.RepeatRows)+cap(plan.summary.RepeatColumns))*64
}
func (plan *Plan) Extent(ctx context.Context, axis string, first, last int) (workbook.PrintGeometryExtent, error) {
	geometry, err := plan.Geometry(ctx)
	if err != nil {
		return workbook.PrintGeometryExtent{}, err
	}
	return geometry.Extent(ctx, axis, first, last)
}

// A page reader validates once, uses this immutable view for all its source
// row/column metrics, and validates again before publishing cell data. It must
// retain the same source pin; there is no file IO per individual metric lookup.
func (plan *Plan) Geometry(ctx context.Context) (*workbook.PrintGeometryView, error) {
	if err := plan.Validate(ctx); err != nil {
		return nil, err
	}
	if plan.geometry == nil {
		return nil, errors.New("空预览没有可用打印几何")
	}
	return plan.geometry, nil
}
func (plan *Plan) Page(ctx context.Context, index int) (Page, error) {
	if err := plan.Validate(ctx); err != nil {
		return Page{}, err
	}
	if index < 0 || index >= len(plan.pages) {
		return Page{}, errors.New("打印预览页码越界")
	}
	page := plan.pages[index]
	page.Rows = slices.Clone(page.Rows)
	page.Columns = slices.Clone(page.Columns)
	return page, nil
}

type geometry interface {
	Extent(context.Context, string, int, int) (workbook.PrintGeometryExtent, error)
}
type work struct {
	ctx         context.Context
	calls       int
	approximate bool
	defaulted   bool
}
type axisBand struct {
	parts    []Part
	used     int64
	oversize bool
}
