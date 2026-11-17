package formats

// Composition describes explicit read-only operations, independently of the
// row/column capacity of each input format. Results are streamed, not retained
// as one in-memory table.
const (
	CompositionSources             = 8
	CompositionColumns             = 256
	CompositionDefinitionBytes     = 1 << 20
	CompositionRequestBytes        = CompositionDefinitionBytes + (8 << 10)
	CompositionRecordBytes         = 8 << 20
	CompositionSourceRowBytes      = 32 << 20
	CompositionSourceBytes         = 64 << 20
	CompositionPreviewRows         = 100
	CompositionPreviewBytes        = 4 << 20
	CompositionResultBytes         = 8 << 20
	CompositionActiveTasks         = 2
	CompositionTaskHistory         = 4
	CompositionSnapshots           = 2
	CompositionRetainedBytes       = 160 << 20
	CompositionDefinitions         = 32
	CompositionCatalogBytes        = 8 << 20
	CompositionCatalogRequestBytes = 8 << 10
	CompositionFieldPageRows       = 20
	CompositionPickerRows          = 40
	CompositionTypeSampleRows      = 8
	SourceAttachmentFiles          = 4
)

type CompositionLimits struct {
	Sources       int `json:"sources"`
	Columns       int `json:"columns"`
	PreviewRows   int `json:"previewRows"`
	RequestBytes  int `json:"requestBytes"`
	ResultBytes   int `json:"resultBytes"`
	Attachments   int `json:"attachments"`
	FieldPageRows int `json:"fieldPageRows"`
	PickerRows    int `json:"pickerRows"`
}

func Composition() CompositionLimits {
	return CompositionLimits{CompositionSources, CompositionColumns, CompositionPreviewRows, CompositionRequestBytes, CompositionResultBytes, SourceAttachmentFiles, CompositionFieldPageRows, CompositionPickerRows}
}
