package workbook

import (
	"archive/zip"

	"excellens/internal/styles"
)

type StyleInfo = styles.StyleInfo
type VisualStyle = styles.VisualStyle
type CalendarNumberFormat = styles.CalendarNumberFormat
type RichText = styles.RichText
type RichTextRun = styles.RichTextRun
type RichFont = styles.RichFont
type DifferentialStyle = styles.DifferentialStyle
type DifferentialBorders = styles.DifferentialBorders
type DifferentialBorderSide = styles.DifferentialBorderSide
type DifferentialNumberFormat = styles.DifferentialNumberFormat

func XLSXCalendarStyle(data []byte, index int) (string, error) {
	return styles.XLSXCalendarStyle(data, index)
}

func XLSXNumberLabels(file *zip.File, date1904 bool) (func(string, int) string, error) {
	return styles.XLSXNumberLabels(file, maxMetadataPart, date1904)
}

func XLSXCalendarFormats(data []byte) (map[string]CalendarNumberFormat, error) {
	return styles.XLSXCalendarFormats(data)
}

func CalendarDateNumberFormat(id int, code string) bool {
	return styles.CalendarDateNumberFormat(id, code)
}

func ValidateRichText(text string, value *RichText) error {
	return styles.ValidateRichText(text, value)
}

func (w *Workbook) readStyles(f *zip.File) error {
	table, err := styles.ReadOOXMLStyles(f, maxMetadataPart, w.ThemeColors)
	if err != nil {
		return err
	}
	w.applyResolvedStyles(table)
	return nil
}

func (w *Workbook) applyStyleTable(sx styles.StyleSheetXML) {
	w.applyResolvedStyles(styles.BuildTable(sx, w.ThemeColors))
}

func (w *Workbook) applyResolvedStyles(table styles.Table) {
	w.normalFont = table.NormalFont
	w.NumFmts = table.NumFmts
	w.IndexedColors = table.IndexedColors
	w.differentialStyles = table.DifferentialStyles
	w.differentialNotice = table.DifferentialNotice
	w.Styles = table.Styles
	w.VisualStyles = table.VisualStyles
}

func (w *Workbook) resolveColor(c *styles.ColorXML, indexed []string) string {
	return styles.ResolveColor(c, w.ThemeColors, indexed)
}

func (w *Workbook) resolveRichFont(source *styles.RichFontXML) (RichFont, bool) {
	return styles.ResolveRichFont(source, w.ThemeColors, w.IndexedColors)
}

func (w *Workbook) formatNumeric(raw string, style int) string {
	return styles.FormatNumeric(raw, style, w.Styles, w.NumFmts, w.Date1904)
}

func (w *Workbook) FormatNumericID(raw string, fmtID int) string {
	return styles.FormatNumericID(raw, fmtID, w.NumFmts, w.Date1904)
}

// PresentNumeric applies the workbook's number format and color to a draft
// value without changing the source model or its row cache.
func (w *Workbook) PresentNumeric(cell *CellData) {
	cell.Display = w.formatNumeric(cell.Raw, cell.Style)
	cell.ValueColor = w.numericTextColor(cell.Raw, cell.Style)
}

func (w *Workbook) numericTextColor(raw string, style int) string {
	return styles.NumericTextColor(raw, style, w.Styles)
}

func (w *Workbook) GridShortDate(id int) string {
	return styles.GridShortDate(id, w.NumFmts)
}
