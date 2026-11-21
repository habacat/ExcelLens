package formats

// Relation limits bound explicit two-source operations, independently of each
// input format. Preflight may count a larger Cartesian result without emitting it.
const (
	ReconcileToleranceChars = 128
	VersionAlignmentRows    = ExcelRows
	BatchArchiveBytes       = 256 << 20
	BatchDetailBytes        = 512 << 20
	AuditReportBytes        = 1 << 30
	BatchCSVRowBytes        = 32 << 20
	ReconcileRecordBytes    = 1 << 20
	RelationKeys            = 4
	RelationColumns         = 64 // per source, excluding additional key columns
	RelationPreviewRows     = 100
	RelationPreviewBytes    = 4 << 20
	RelationRecordBytes     = 8 << 20
	RelationSourceRowBytes  = 32 << 20
	RelationOutputRows      = ExternalRows
	RelationDefaultRows     = 1_000_000
	RelationActiveTasks     = 1
	RelationTaskHistory     = 4
	// Retained Go heap estimates exclude shared workbook indexes, mapped pages
	// and transient readers/sort work. Temporary files use the query disk quota.
	RelationSnapshotBytes = 96 << 20
	RelationRetainedBytes = 192 << 20
	RelationRequestBytes  = 16 << 10
	RelationResultBytes   = 8 << 20
	RelationViewRows      = 20
	RelationViewColumns   = 12
	RelationInitialFields = 8
	RelationSnippetUnits  = 256
	// Per-export reuse of immutable type/state encodings; never source values.
	RelationCSVCacheEntries = 64
	RelationCSVCacheBytes   = 256 << 10
	RelationZipRows         = 100_000
)
