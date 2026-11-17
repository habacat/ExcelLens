package printing

import (
	"context"
	"errors"
	"math/big"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type region struct {
	rows, columns                                          []Interval
	rowTitles, columnTitles                                []Part
	rowTitleUnits, columnTitleUnits, rowUnits, columnUnits int64
}

func New(ctx context.Context, source *workbook.PrintSource, options Options) (*Plan, error) {
	if err := source.Validate(ctx); err != nil {
		return nil, err
	}
	r, err := resolve(source, options)
	if err != nil {
		return nil, err
	}
	if len(r.areas) == 0 {
		if err := source.Validate(ctx); err != nil {
			return nil, err
		}
		return &Plan{source: source, summary: Summary{SourceID: source.Identity(), Sheet: source.Sheet(), Origins: r.origins, Notices: r.notices}}, nil
	}
	g, err := source.Geometry().ResolveDefaults(ctx, r.defaults)
	if err != nil {
		return nil, err
	}
	w := &work{ctx: ctx}
	width := r.paper.Width - r.margins.Left - r.margins.Right - r.headingWidth
	height := r.paper.Height - r.margins.Top - r.margins.Bottom - r.headingHeight
	if width <= 0 || height <= 0 {
		return nil, errors.New("纸张扣除边距及行列标题后没有正文空间")
	}
	rowTitles, rowTitleUnits, err := w.parts(g, "row", r.rowTitles, true)
	if err != nil {
		return nil, err
	}
	columnTitles, columnTitleUnits, err := w.parts(g, "col", r.columnTitles, true)
	if err != nil {
		return nil, err
	}
	regions := make([]region, 0, len(r.areas))
	for _, area := range r.areas {
		rows, columns := subtract(Interval{area.Row, area.EndRow}, r.rowTitles), subtract(Interval{area.Col, area.EndCol}, r.columnTitles)
		_, rowUnits, err := w.parts(g, "row", rows, false)
		if err != nil {
			return nil, err
		}
		_, columnUnits, err := w.parts(g, "col", columns, false)
		if err != nil {
			return nil, err
		}
		regions = append(regions, region{rows, columns, rowTitles, columnTitles, rowTitleUnits, columnTitleUnits, rowUnits, columnUnits})
		for _, title := range r.rowTitles {
			if title.First > area.Row && title.First <= area.EndRow {
				r.notice("内部重复标题按源定义置于每页开头，正文不重复计入该标题；源坐标保留")
			}
		}
		for _, title := range r.columnTitles {
			if title.First > area.Col && title.First <= area.EndCol {
				r.notice("内部重复列按源定义置于每页左侧，正文不重复计入该标题；源坐标保留")
			}
		}
	}
	if r.fit != nil {
		r.scale = big.NewRat(4, 1)
		if r.fit.Width == 0 && r.fit.Height == 0 {
			r.scale = big.NewRat(1, 1)
		}
		for _, region := range regions {
			for _, axis := range []struct {
				name                    string
				body                    []Interval
				titles                  []Part
				title, total, available int64
				pages                   int
			}{{"row", region.rows, rowTitles, rowTitleUnits, region.rowUnits, height, r.fit.Height}, {"col", region.columns, columnTitles, columnTitleUnits, region.columnUnits, width, r.fit.Width}} {
				if axis.pages == 0 {
					continue
				}
				capacity, err := w.fitCapacity(g, axis.name, axis.body, axis.titles, axis.title, axis.total, axis.pages)
				if err != nil {
					return nil, err
				}
				if capacity == 0 {
					continue
				}
				candidate := big.NewRat(axis.available, capacity)
				if candidate.Cmp(r.scale) < 0 {
					r.scale = candidate
				}
			}
		}
		if len(regions) > 1 {
			r.notice("多个打印区域使用同一缩放比例，分别适配页数")
		}
	}
	if r.scale == nil || r.scale.Sign() <= 0 {
		return nil, errors.New("未取得有效打印缩放比例")
	}
	axisWidth, err := unscaled(width, r.scale)
	if err != nil {
		return nil, err
	}
	axisHeight, err := unscaled(height, r.scale)
	if err != nil {
		return nil, err
	}
	plan := &Plan{source: source, geometry: g}
	parts := 0
	for index, region := range regions {
		if region.rowUnits == 0 && rowTitleUnits == 0 || region.columnUnits == 0 && columnTitleUnits == 0 {
			continue
		}
		rows, _, err := w.pack(g, "row", region.rows, rowTitles, rowTitleUnits, axisHeight, r.rowBreaks, 0, true)
		if err != nil {
			return nil, err
		}
		columns, _, err := w.pack(g, "col", region.columns, columnTitles, columnTitleUnits, axisWidth, r.columnBreaks, 0, true)
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 && len(columns) > (formats.PrintPaginationPages-len(plan.pages))/len(rows) {
			return nil, errors.New("打印预览总页数超过容量，未发布部分页")
		}
		appendPage := func(row, column axisBand) error {
			parts += len(row.parts) + len(column.parts)
			if parts > formats.PrintPaginationParts || int64(parts)*64+int64(len(plan.pages)+1)*256 > formats.PrintPaginationBytes {
				return errors.New("打印分页结果超过片段或内存容量")
			}
			x, err := scaled(column.used, r.scale)
			if err != nil {
				return err
			}
			y, err := scaled(row.used, r.scale)
			if err != nil {
				return err
			}
			bodyWidth, err := scaled(column.used-columnTitleUnits, r.scale)
			if err != nil {
				return err
			}
			bodyHeight, err := scaled(row.used-rowTitleUnits, r.scale)
			if err != nil {
				return err
			}
			offsetX, offsetY := r.margins.Left+r.headingWidth, r.margins.Top+r.headingHeight
			if r.centerX {
				offsetX += (width - x) / 2
			}
			if r.centerY {
				offsetY += (height - y) / 2
			}
			oversize := row.oversize || column.oversize
			if oversize {
				r.notice("存在超出单页的源行列，预览保留跨页物理切片，源内容未截断")
			}
			plan.pages = append(plan.pages, Page{Number: r.first + int64(len(plan.pages)), Area: index + 1, Rows: row.parts, Columns: column.parts, Body: Dimensions{bodyWidth, bodyHeight}, Content: Dimensions{x, y}, OffsetX: offsetX, OffsetY: offsetY, Oversize: oversize})
			return nil
		}
		if r.order == "downThenOver" {
			for _, column := range columns {
				for _, row := range rows {
					if err := appendPage(row, column); err != nil {
						return nil, err
					}
				}
			}
		} else {
			for _, row := range rows {
				for _, column := range columns {
					if err := appendPage(row, column); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if err := source.Validate(ctx); err != nil {
		return nil, err
	}
	if w.defaulted {
		r.notice("源未保存的默认行列尺寸使用预览缺省值，来源标记见设置；未改写源几何")
	}
	percentage := new(big.Rat).Mul(r.scale, big.NewRat(100, 1))
	plan.summary = Summary{SourceID: source.Identity(), Sheet: source.Sheet(), Pages: len(plan.pages), Paper: r.paper, Margins: r.margins, Scale: Scale{r.scale.Num().String(), r.scale.Denom().String(), percentage.FloatString(6)}, Approximate: w.approximate || len(r.notices) > 0, Origins: r.origins, Notices: r.notices}
	plan.summary.Areas = r.areas
	plan.summary.RepeatRows = r.rowTitles
	plan.summary.RepeatColumns = r.columnTitles
	plan.summary.PageOrder = r.order
	plan.summary.FirstPageNumber = r.first
	plan.summary.Headings = Dimensions{r.headingWidth, r.headingHeight}
	plan.summary.Defaults = r.defaults
	return plan, nil
}
