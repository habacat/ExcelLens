package formats

const ExportArchiveChunkBytes = 64 << 10

type RelationExportOption struct {
	Format     string `json:"format"`
	Name       string `json:"name"`
	Compressed bool   `json:"compressed"`
}

func RelationExports() []RelationExportOption {
	return []RelationExportOption{{Format: "csv", Name: "CSV"}, {Format: "zipcsv", Name: "ZIP", Compressed: true}}
}

func RelationExportFormat(value string) (string, bool) {
	options := RelationExports()
	if value == "" {
		value = options[0].Format // Existing API requests keep their CSV output.
	}
	for _, option := range options {
		if option.Format == value {
			return value, true
		}
	}
	return "", false
}
