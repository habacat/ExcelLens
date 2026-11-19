package formats

import "strings"

const (
	EditSourceBytes           int64 = 512 << 20
	EditStructureHistoryBytes int64 = 4 << 30
	EditOutputBytes           int64 = 768 << 20
	EditTextRows                    = 2_000_000
	EditTextStride                  = 512
	EditRecordBytes                 = 8 << 20
	EditCellBytes                   = 256 << 10
	EditFormulaBytes                = 4096
	EditBatchCells                  = 10_000
	EditChangedCells                = 50_000
	EditHistoryBytes                = 32 << 20
	EditHistorySteps                = 4096
	EditRequestBytes                = 4 << 20
	EditOutputVersions              = 8
	EditValidationRules             = 512
	EditRuleTextBytes               = 1024
	EditInlineListUnits             = 255
	EditReferenceListCells          = 256
	EditPivotChoicesPage            = 128
	EditReferenceListBytes          = 64 << 10
	EditReferenceCells              = 10_000
	EditXLSXStyleIndex              = 65429
	EditXLSXParts                   = 256
	EditXLSXDirectoryBytes          = 64
	EditXLSXRangeLimit              = 16_384
	EditCalcSourceCells             = 500_000
	EditCalcReferences              = 1_000_000
	EditCalcVisits                  = 12_000_000
	EditCalcFormulaBytes            = 16 << 20
	EditCalcProjectionBytes         = 48 << 20
	EditXLSXPartBytes               = 48 << 20
	EditXLSXPackageBytes            = 256 << 20
	// Larger Office packages use coordinate-local reads and a streaming writer.
	EditOfficeParts          = 4096
	EditOfficePartBytes      = 1 << 30
	EditOfficePackageBytes   = 4 << 30
	EditOfficeLocalScanBytes = 128 << 20
)

// EditExtension advertises candidates. Each individual file still requires
// successful format-specific preflight before a document can be created.
func EditExtension(ext string) string {
	switch strings.ToLower(ext) {
	case ".csv":
		return "csv"
	case ".tsv":
		return "tsv"
	case ".xlsx":
		return "xlsx"
	case ".xlsm":
		return "xlsm"
	default:
		return ""
	}
}

func EditOOXML(format string) bool { return format == "xlsx" || format == "xlsm" }

type EditLimits struct {
	Extensions   []string `json:"extensions"`
	CellBytes    int      `json:"cellBytes"`
	FormulaBytes int      `json:"formulaBytes"`
	BatchCells   int      `json:"batchCells"`
	RequestBytes int      `json:"requestBytes"`
}

func Editing() EditLimits {
	limits := EditLimits{CellBytes: EditCellBytes, FormulaBytes: EditFormulaBytes, BatchCells: EditBatchCells, RequestBytes: EditRequestBytes}
	for _, ext := range Extensions() {
		if EditExtension(ext) != "" {
			limits.Extensions = append(limits.Extensions, ext)
		}
	}
	return limits
}
