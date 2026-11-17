package formats

// Explicit selection analysis is separate from the source format's capacity.
// It retains aggregate segments, never a vector of all selected values.
const (
	SelectionQuickCells        = 400_000
	SelectionQuickColumns      = 16
	SelectionQuickSegments     = 60
	SelectionQuickRequestBytes = 8 << 10
	SelectionQuickResultBytes  = 4 << 20
	SelectionQuickSourceBytes  = 64 << 20
	SelectionQuickTextBytes    = 32 << 20
)

type SelectionQuickLimits struct {
	Cells        int `json:"cells"`
	Columns      int `json:"columns"`
	Segments     int `json:"segments"`
	RequestBytes int `json:"requestBytes"`
	Bytes        int `json:"bytes"`
}

func SelectionQuick() SelectionQuickLimits {
	return SelectionQuickLimits{SelectionQuickCells, SelectionQuickColumns, SelectionQuickSegments, SelectionQuickRequestBytes, SelectionQuickResultBytes}
}
