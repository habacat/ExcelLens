package formats

// Sort runs share the process query-file quota; candidates have their own
// conservative structure/text charge, separate from the reader's memory.
const (
	ClusterTextBytes      = 128 << 10
	ClusterGroups         = 10000
	ClusterVariants       = 20000
	ClusterSampleRows     = 8
	ClusterResultBytes    = 32 << 20
	ClusterRetainedBytes  = 64 << 20
	ClusterActiveTasks    = 2
	ClusterTaskHistory    = 4
	ClusterSnapshots      = 2
	ClusterPageGroups     = 25
	ClusterPageVariants   = 50
	ClusterRequestBytes   = 2 << 20
	TextMappingEntries    = 2500
	TextMappingBytes      = 1 << 20
	TextMappingValueRunes = 4000
)

type ClusteringLimits struct {
	PageGroups   int `json:"pageGroups"`
	PageVariants int `json:"pageVariants"`
}

func Clustering() ClusteringLimits {
	return ClusteringLimits{PageGroups: ClusterPageGroups, PageVariants: ClusterPageVariants}
}
