package formats

// Snapshots retain an explicitly selected completed aggregate, never source
// records. An oversized result must be narrowed by the user, not truncated.
const (
	SnapshotEntries      = 24
	SnapshotRows         = 200
	SnapshotSeries       = 12
	SnapshotCells        = 2400
	SnapshotBytes        = 1 << 20
	SnapshotStoreBytes   = 16 << 20
	SnapshotRequestBytes = 8 << 10
	SnapshotPageRows     = 40
	SnapshotExactChars   = 8192
)

type SnapshotLimits struct {
	Entries    int `json:"entries"`
	Rows       int `json:"rows"`
	Series     int `json:"series"`
	Bytes      int `json:"bytes"`
	StoreBytes int `json:"storeBytes"`
	PageRows   int `json:"pageRows"`
}

func Snapshots() SnapshotLimits {
	return SnapshotLimits{SnapshotEntries, SnapshotRows, SnapshotSeries, SnapshotBytes, SnapshotStoreBytes, SnapshotPageRows}
}
