package analysis

import "fmt"

// ValidateWorkspace is used before restoring external JSON. A corrupt backup
// must never overwrite a valid user workspace.
func ValidateWorkspace(w Workspace) error {
	if err := validateWorkspaceScopes(w); err != nil {
		return err
	}
	if len(w.Sheets) > MaxSheets || len(w.Reports) > MaxReports || len(w.DashboardOrder) > MaxReports {
		return fmt.Errorf("工作区格式或对象数量无效")
	}
	ids := make(map[string]bool, len(w.Sheets)+len(w.Reports))
	for _, s := range w.Sheets {
		if s.ID == "" || ids["sheet:"+s.ID] || !NameValid(s.Name) || len(s.Columns) < 1 || len(s.Columns) > MaxSheetCols || len(s.Rows) > MaxSheetRows || s.Revision < 0 {
			return fmt.Errorf("备份编辑表名称、版本或尺寸无效")
		}
		ids["sheet:"+s.ID] = true
		for _, c := range s.Columns {
			if !NameValid(c) {
				return fmt.Errorf("编辑表列标题无效")
			}
		}
		for _, row := range s.Rows {
			if len(row) > len(s.Columns) {
				return fmt.Errorf("编辑表行数据超过列数")
			}
			for _, cell := range row {
				if !ValidCellText(cell) {
					return fmt.Errorf("单元格内容过长")
				}
			}
		}
	}
	for _, r := range w.Reports {
		if r.ID == "" || ids["report:"+r.ID] || !NameValid(r.Name) {
			return fmt.Errorf("报表名称或标识无效")
		}
		ids["report:"+r.ID] = true
		if r.Query.Source == "" || len(r.Query.Filters) > 8 || len(r.Query.Metrics) > 6 {
			return fmt.Errorf("报表查询配置无效")
		}
	}
	for _, id := range w.DashboardOrder {
		if !ids["report:"+id] {
			return fmt.Errorf("仪表盘引用不存在的报表")
		}
	}
	return nil
}

func validateWorkspaceScopes(w Workspace) error {
	if w.Version < 1 || w.Version > 6 {
		return fmt.Errorf("分析工作区格式版本不支持")
	}
	for _, report := range w.Reports {
		if err := ValidateSource(report.Query); err != nil {
			return err
		}
		if report.Query.Composition != nil && w.Version < 6 {
			return fmt.Errorf("联合来源报表要求分析工作区版本6，原文件已保留")
		}
		for _, step := range report.Query.Transforms {
			if err := ValidateTextMapping(step); err != nil {
				return err
			}
		}
		if HasTextMappings(report.Query) && w.Version < 5 {
			return fmt.Errorf("文本映射报表要求分析工作区版本5，原文件已保留")
		}
		if err := ValidateComparisons(report.Query); err != nil {
			return err
		}
		if HasComparisons(report.Query) && w.Version < 4 {
			return fmt.Errorf("占比或累计报表要求分析工作区版本4，原文件已保留")
		}
		if err := validateLayout(report.Query); err != nil {
			return err
		}
		if report.Query.Layout == "tree" && w.Version < 3 {
			return fmt.Errorf("层级透视报表要求分析工作区版本3，原文件已保留")
		}
		if report.Query.SourceScope != nil {
			if w.Version < 2 {
				return fmt.Errorf("来源范围报表要求分析工作区版本2，原文件已保留")
			}
			if err := validateSourceScope(report.Query); err != nil {
				return err
			}
		}
	}
	return nil
}
