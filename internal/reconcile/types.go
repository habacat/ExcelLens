// Package reconcile compares and explicitly relates immutable, typed table
// snapshots. Comparisons never automatically pair duplicate keys; relations
// preflight their complete output count before bounded extraction. Neither
// operation interprets a discrepancy as an error, loss or accounting conclusion.
package reconcile

import (
	"context"
	"iter"

	"excellens/internal/formats"
)

const (
	MaxKeys        = formats.RelationKeys
	MaxFields      = 8
	MaxRecordBytes = 8 << 20
	MaxKeyBytes    = 1 << 20
	MaxSamples     = 100
	MaxSampleBytes = 4 << 20
	SampleChars    = 256
)

type Value struct {
	Raw          string `json:"raw"`
	Display      string `json:"display,omitempty"`
	Type         string `json:"type,omitempty"`
	Exists       bool   `json:"exists"`
	MissingCache bool   `json:"missingCache,omitempty"`
	Uncertain    string `json:"uncertain,omitempty"`
	TypeMissing  bool   `json:"typeMissing,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
}

type Record struct {
	Position int           `json:"position"`
	Number   int           `json:"number"`
	Values   map[int]Value `json:"values"`
}

type Field struct {
	Column       int    `json:"column"`
	Name         string `json:"name"`
	RawName      string `json:"rawName,omitempty"`
	HeaderHash   string `json:"headerHash,omitempty"`
	HeaderLoaded bool   `json:"headerLoaded"`
	Truncated    bool   `json:"truncated,omitempty"`
	UniqueHeader bool   `json:"uniqueHeader,omitempty"`
}

type SourceInfo struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Sheet       string  `json:"sheet"`
	Kind        string  `json:"kind"`
	Format      string  `json:"format,omitempty"`
	Notice      string  `json:"notice,omitempty"`
	Fingerprint string  `json:"fingerprint"`
	Stamp       string  `json:"stamp"`
	HeaderRow   int     `json:"headerRow"`
	Fields      []Field `json:"fields"`
	Total       int     `json:"total"`
}

type Source struct {
	Info       SourceInfo
	SourceRows int
	// Conservative Go heap held by detached source values, excluding Info and
	// shared workbook/index storage. Ordinary file adapters retain no value array.
	RetainedBytes int64
	// Relations require a source identity check before and after each operation.
	// Existing comparison snapshots retain their established cache semantics.
	Check func(context.Context) error
	Scan  func(context.Context, []int, func(Record) error) error
	Read  func(context.Context, iter.Seq[int], []int, func(Record) error) error
}

// Empty Mode preserves source type; text explicitly uses displayed text and
// number explicitly parses a strict exact accounting amount/percentage.
type Pair struct {
	Tolerance  string `json:"tolerance,omitempty"`
	Left       int    `json:"left"`
	Right      int    `json:"right"`
	Mode       string `json:"mode,omitempty"`
	Trim       bool   `json:"trim,omitempty"`
	IgnoreCase bool   `json:"ignoreCase,omitempty"`
}

const CurrentRuleVersion = 2

type Query struct {
	RuleVersion  int    `json:"ruleVersion,omitempty"`
	Keys         []Pair `json:"keys"`
	Fields       []Pair `json:"fields"`
	IncludeEqual bool   `json:"includeEqual,omitempty"`
}

func (q Query) Rules() int {
	if q.RuleVersion == 0 {
		return CurrentRuleVersion
	}
	return q.RuleVersion
}

type Comparison struct {
	WithinTolerance bool   `json:"withinTolerance,omitempty"`
	Pair            int    `json:"pair"`
	Equal           bool   `json:"equal"`
	Delta           string `json:"delta,omitempty"` // exact left minus right, never float64
	Reason          string `json:"reason,omitempty"`
}

type Group struct {
	KeyID         string       `json:"keyId"`
	Key           []string     `json:"key"`
	Status        string       `json:"status"`
	LeftCount     int64        `json:"leftCount"`
	RightCount    int64        `json:"rightCount"`
	Left          *Record      `json:"left,omitempty"`
	Right         *Record      `json:"right,omitempty"`
	LeftRows      []int        `json:"leftRows,omitempty"`
	RightRows     []int        `json:"rightRows,omitempty"`
	Comparisons   []Comparison `json:"comparisons,omitempty"`
	Reason        string       `json:"reason,omitempty"`
	TextTruncated bool         `json:"textTruncated,omitempty"`
}

type Counts struct {
	WithinTolerance int64 `json:"withinTolerance"`
	Equal           int64 `json:"equal"`
	Different       int64 `json:"different"`
	LeftOnly        int64 `json:"leftOnly"`
	RightOnly       int64 `json:"rightOnly"`
	Duplicate       int64 `json:"duplicate"`
	Review          int64 `json:"review"`
	InvalidKeys     int64 `json:"invalidKeys"` // rows, not distinct valid keys
	LeftRecords     int64 `json:"leftRecords"`
	RightRecords    int64 `json:"rightRecords"`
}

type Result struct {
	Left             SourceInfo `json:"left"`
	Right            SourceInfo `json:"right"`
	Query            Query      `json:"query"`
	Counts           Counts     `json:"counts"`
	Samples          []Group    `json:"samples"`
	SamplesTruncated bool       `json:"samplesTruncated"`
}

// Sinks are synchronous and borrow the current records. A duplicate member is
// emitted once, followed by a group summary with the final two-sided counts.
// This preserves every duplicate row without buffering the entire key group.
type Sinks struct {
	Group     func(Group) error
	Duplicate func(keyID string, side string, record Record) error
}

type Progress func(stage string, done, total int64)
