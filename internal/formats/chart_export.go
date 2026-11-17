package formats

// Image generation is local to the browser and independent of source capacity.
// Keep pixels, encoded artifacts and exact-data attachments separately bounded.
type ChartExportLimits struct {
	Types         []string `json:"types"`
	Scales        []int    `json:"scales"`
	MinWidth      int      `json:"minWidth"`
	MaxWidth      int      `json:"maxWidth"`
	MinHeight     int      `json:"minHeight"`
	MaxHeight     int      `json:"maxHeight"`
	DefaultWidth  int      `json:"defaultWidth"`
	DefaultHeight int      `json:"defaultHeight"`
	Pixels        int      `json:"pixels"`
	DataBytes     int      `json:"dataBytes"`
	ImageBytes    int      `json:"imageBytes"`
	SVGBytes      int      `json:"svgBytes"`
	SVGNodes      int      `json:"svgNodes"`
	PackageBytes  int      `json:"packageBytes"`
}

func ChartExport() ChartExportLimits {
	return ChartExportLimits{Types: []string{"png", "svg"}, Scales: []int{1, 2},
		MinWidth: 640, MaxWidth: 2400, MinHeight: 480, MaxHeight: 1600,
		DefaultWidth: 1200, DefaultHeight: 800, Pixels: 20_000_000,
		DataBytes: 16 << 20, ImageBytes: 20 << 20, SVGBytes: 4 << 20,
		SVGNodes: 20000, PackageBytes: 40 << 20}
}
