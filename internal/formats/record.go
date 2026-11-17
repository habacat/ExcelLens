package formats

// A record card reads one source row and one explicit header row. These are
// operation budgets, independent of the source format's supported capacity.
const (
	RecordCardColumns      = 32
	RecordCardRequestBytes = 8 << 10
	RecordCardPageBytes    = 8 << 20
	RecordCardSourceBytes  = 32 << 20
	RecordCardTextUnits    = 4096
)

type RecordCardLimits struct {
	Columns      int `json:"columns"`
	Bytes        int `json:"bytes"`
	RequestBytes int `json:"requestBytes"`
	TextUnits    int `json:"textUnits"`
}

func RecordCard() RecordCardLimits {
	return RecordCardLimits{RecordCardColumns, RecordCardPageBytes, RecordCardRequestBytes, RecordCardTextUnits}
}
