package formats

// Explicit single-cell inspection is independent of record-card page budgets.
const (
	CellExplanationRequestBytes = 8 << 10
	CellExplanationBytes        = 2 << 20
	CellExplanationSourceBytes  = 32 << 20
)

type CellExplanationLimits struct {
	Bytes        int `json:"bytes"`
	RequestBytes int `json:"requestBytes"`
}

func CellExplanation() CellExplanationLimits {
	return CellExplanationLimits{CellExplanationBytes, CellExplanationRequestBytes}
}
