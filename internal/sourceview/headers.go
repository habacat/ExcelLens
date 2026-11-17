package sourceview

// HeaderIdentity describes a complete raw-header identity, not its shortened
// display label. Unique is false when the adapter has not inspected the whole
// header and therefore cannot prove that another same-named column is absent.
type HeaderIdentity struct {
	ID, Hash string
	Complete bool
	Unique   bool
}

type HeaderMatch struct {
	Field      string   `json:"field"`
	Target     string   `json:"target"`
	Status     string   `json:"status"`
	Candidates []string `json:"candidates"`
}

// MatchHeaders never resolves duplicates by position or compares shortened
// labels. Callers can add type compatibility checks after a unique name match.
func MatchHeaders(expected, actual []HeaderIdentity) []HeaderMatch {
	counts := make(map[string]int, len(expected))
	expectedIDs := make(map[string]int, len(expected))
	for _, field := range expected {
		expectedIDs[field.ID]++
		if field.Complete && field.Hash != "" {
			counts[field.Hash]++
		}
	}
	byHash := make(map[string][]HeaderIdentity, len(actual))
	actualIDs := make(map[string]int, len(actual))
	for _, field := range actual {
		actualIDs[field.ID]++
		if field.Complete && field.Hash != "" {
			byHash[field.Hash] = append(byHash[field.Hash], field)
		}
	}
	out := make([]HeaderMatch, 0, len(expected))
	for _, field := range expected {
		match := HeaderMatch{Field: field.ID, Status: "missing", Candidates: []string{}}
		var candidates []HeaderIdentity
		if field.Complete && field.Hash != "" {
			candidates = byHash[field.Hash]
			for _, candidate := range candidates {
				match.Candidates = append(match.Candidates, candidate.ID)
			}
		}
		switch {
		case !field.Complete || field.Hash == "" || field.ID == "":
			match.Status = "manual"
		case !field.Unique || expectedIDs[field.ID] > 1 || counts[field.Hash] > 1 || len(candidates) > 1 || len(candidates) == 1 && (!candidates[0].Unique || candidates[0].ID == "" || actualIDs[candidates[0].ID] > 1):
			match.Status = "ambiguous"
		case len(candidates) == 1:
			match.Status, match.Target = "match", candidates[0].ID
		}
		out = append(out, match)
	}
	return out
}
