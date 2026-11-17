package formats

// Durable task checkpoints are separate from disposable query runs and the
// small page-input recovery journal. All processes share these disk limits.
const (
	CheckpointTasks                 = 32
	CheckpointSources               = 8
	CheckpointFiles                 = 4096
	CheckpointBytes           int64 = 8 << 30
	CheckpointManifestBytes         = 256 << 10
	CheckpointDefinitionBytes       = 128 << 10
	CheckpointCursorBytes           = 64 << 10
	CheckpointBlockBytes      int64 = 128 << 20
	CheckpointIOBytes               = 64 << 10
	CheckpointTitleRunes            = 160
	CheckpointFileNameBytes         = 2048
	CheckpointSearchRows            = 4096
	CheckpointFilterRows            = 4096
	CheckpointOrderRows             = 65536
	CheckpointSearchHits            = 500
	CheckpointSearchBytes           = 16 << 20
	CheckpointRequestBytes          = 64 << 10
	CheckpointStreamReserve   int64 = 1 << 20
	ExportRecoveryRows              = 4096
	ExportPartChunkBytes            = 64 << 10
	ExportRecoveryMetadata          = 64 << 10
)
