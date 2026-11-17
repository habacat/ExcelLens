package formats

const (
	ProfileCells           = 4_000_000
	ProfileRequestBytes    = 8 << 10
	ProfileResultBytes     = 2 << 20
	ProfileSourceBytes     = 512 << 20
	ProfileSourceRowBytes  = 32 << 20
	ProfileTextBytes       = 128 << 20
	ProfileFrequencyBytes  = 32 << 20
	ProfileSortMemoryBytes = 8 << 20
	ProfileDistinct        = 10000
	ProfileTopValues       = 12
	ProfileHistogramBins   = 10
	RationalSortKeyBytes   = 4096
)

type ProfileLimits struct {
	Cells        int `json:"cells"`
	RequestBytes int `json:"requestBytes"`
	Bytes        int `json:"bytes"`
	Bins         int `json:"bins"`
}

func Profile() ProfileLimits {
	return ProfileLimits{ProfileCells, ProfileRequestBytes, ProfileResultBytes, ProfileHistogramBins}
}
