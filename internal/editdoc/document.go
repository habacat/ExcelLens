// Package editdoc stores typed, sparse copy edits and durable undo/redo. It
// does not open the base file, interpret formulas, or write a user target.
package editdoc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"maps"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"excellens/internal/checkpoint"
	"excellens/internal/formats"
)

const driver = "edit-document"

// GridLayoutSemantics is the first generation with the current coordinate
// preflight. Generation 8 added a source XF for number formats. Generation 9
// can also retain a formula's built-in format when no source XF supplies it.
const GridLayoutSemantics = 7
const CurrentSemantics = 9

var (
	ErrInvalid  = errors.New("编辑操作或单元格类型无效")
	ErrNoChange = errors.New("没有可提交的修改")
	ErrHistory  = errors.New("编辑历史已达上限，请保存副本后继续编辑新文件")
	version     = json.RawMessage(`{"version":1}`)
	decimal     = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[Ee][+-]?[0-9]+)?$`)
)

type Address struct {
	Sheet string `json:"sheet"`
	Row   int    `json:"row"`
	Col   int    `json:"col"`
}

type Value struct {
	Kind  string `json:"kind"`
	Raw   string `json:"raw"`
	Style string `json:"style,omitempty"`
	// NumberStyle supplies only the number format. Style continues to own
	// the destination font, border, alignment, protection and other XF data.
	NumberStyle   string `json:"numberStyle,omitempty"`
	BuiltinFormat int    `json:"builtinFormat,omitempty"`
}

func (v Value) Valid() bool {
	if (v.NumberStyle != "" || v.BuiltinFormat != 0) && v.Style == "" {
		return false
	}
	if v.BuiltinFormat != 0 {
		if v.NumberStyle != "" {
			return false
		}
		switch v.BuiltinFormat {
		case 14, 18, 46:
		default:
			return false
		}
	}
	for _, id := range []string{v.Style, v.NumberStyle} {
		if id == "" {
			continue
		}
		i, err := strconv.Atoi(id)
		if err != nil || i < 0 || strconv.Itoa(i) != id {
			return false
		}
	}
	switch v.Kind {
	case "":
		return v.Raw == "" && v.Style != ""
	case "date":
		_, err := ISODateTime(v.Raw)
		return err == nil
	case "blank":
		return v.Raw == ""
	case "text":
		return len(v.Raw) <= formats.EditCellBytes && utf8.ValidString(v.Raw)
	case "number":
		return len(v.Raw) <= 128 && decimal.MatchString(v.Raw)
	case "boolean":
		return v.Raw == "TRUE" || v.Raw == "FALSE"
	case "error":
		switch v.Raw {
		case "#NULL!", "#DIV/0!", "#VALUE!", "#REF!", "#NAME?", "#NUM!", "#N/A", "#GETTING_DATA":
			return true
		}
		return false
	case "formula":
		return len(v.Raw) > 1 && len(v.Raw) <= formats.EditFormulaBytes && v.Raw[0] == '=' && utf8.ValidString(v.Raw)
	default:
		return false
	}
}

// ISODateTime accepts SpreadsheetML date cells without guessing local date notation.
func ISODateTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", time.DateOnly} {
		if t, err := time.Parse(layout, raw); err == nil && t.Year() > 0 {
			return t, nil
		}
	}
	return time.Time{}, ErrInvalid
}

// Apply overlays just the supplied fields, including an explicit style 0.
func (v Value) Apply(base Value) Value {
	if v.Kind == "" {
		v.Kind, v.Raw = base.Kind, base.Raw
	}
	if v.Style == "" {
		v.Style, v.NumberStyle, v.BuiltinFormat = base.Style, base.NumberStyle, base.BuiltinFormat
	}
	return v
}

func (v Value) NumberFormatStyle() string {
	if v.NumberStyle != "" {
		return v.NumberStyle
	}
	return v.Style
}

func (v Value) difference(base Value) Value {
	if (v.Kind == base.Kind || base.Kind == "missing" && v.Kind == "blank") && v.Raw == base.Raw {
		v.Kind, v.Raw = "", ""
	}
	if v.Style == base.Style && v.NumberStyle == base.NumberStyle && v.BuiltinFormat == base.BuiltinFormat {
		v.Style, v.NumberStyle, v.BuiltinFormat = "", "", 0
	}
	return v
}

type Change struct {
	Address
	Value Value `json:"value"`
}

type Operation struct {
	Request           string     `json:"request"`
	Revision          uint64     `json:"revision,string"`
	Action            string     `json:"action"`
	ExpandTable       bool       `json:"expandTable,omitempty"`
	InferNumberFormat bool       `json:"inferNumberFormat,omitempty"`
	EntryAnchor       *Address   `json:"entryAnchor,omitempty"`
	Changes           []Change   `json:"changes,omitempty"`
	Rule              *Rule      `json:"rule,omitempty"`
	Structure         *Structure `json:"structure,omitempty"`
}

type Key struct {
	ID      string `json:"id"`
	Binding string `json:"binding"`
}

type Info struct {
	Key
	Name       string    `json:"name"`
	Revision   uint64    `json:"revision,string"`
	Updated    time.Time `json:"updated"`
	Changed    int       `json:"changed"`
	Rules      int       `json:"rulesChanged,omitempty"`
	Structures int       `json:"structuresChanged,omitempty"`
	Undo       bool      `json:"undo"`
	Redo       bool      `json:"redo"`
}

type Base struct {
	Source checkpoint.Source
	Name   string
	Layout json.RawMessage
}

type Entry struct {
	Info
	Base      Base
	Semantics uint32
}

func EntryFromManifest(m checkpoint.Manifest) (Entry, error) {
	if m.Definition.Kind != "edit" || m.Definition.Driver != driver || m.Definition.Semantics < 1 || m.Definition.Semantics > CurrentSemantics || len(m.Definition.Sources) != 1 ||
		m.Stage != "draft" || !bytes.Equal(m.Cursor, version) || m.Complete || m.Revision != m.Blocks+1 || m.Records != int64(m.Blocks) ||
		m.Blocks > formats.EditHistorySteps || m.DataBytes > formats.EditHistoryBytes {
		return Entry{}, checkpoint.ErrCorrupt
	}
	return Entry{
		Info:      Info{Key: Key{m.ID, m.DefinitionHash}, Name: m.Definition.FileName, Revision: m.Revision, Updated: m.Updated},
		Base:      Base{Source: m.Definition.Sources[0], Name: m.Definition.FileName, Layout: m.Definition.Payload},
		Semantics: m.Definition.Semantics,
	}, nil
}

type patch struct {
	Address
	Before *Value `json:"before,omitempty"`
	After  *Value `json:"after,omitempty"`
}

type transaction struct {
	Request   string          `json:"request"`
	Digest    string          `json:"digest"`
	Action    string          `json:"action"`
	Patches   []patch         `json:"patches,omitempty"`
	Rule      *rulePatch      `json:"rule,omitempty"`
	Templates []templatePatch `json:"templates,omitempty"`
	NextBase  *Base           `json:"nextBase,omitempty"`
}

type receipt struct {
	digest   string
	revision uint64
}

type Document struct {
	Entry
	lease      *checkpoint.Lease
	dataSize   int64
	value      map[Address]Value
	templates  map[Address]string
	rules      map[Area]Rule
	undo       []transaction
	redo       []transaction
	receipts   map[string]receipt
	workspaces []workspace
}

func Create(ctx context.Context, store *checkpoint.Store, base Base) (Entry, error) {
	lease, err := store.Create(ctx, checkpoint.Definition{
		Kind: "edit", Driver: driver, Semantics: CurrentSemantics, Title: "编辑副本", FileName: base.Name,
		Sources: []checkpoint.Source{base.Source}, Payload: base.Layout,
	}, "draft", version)
	if err != nil {
		return Entry{}, err
	}
	defer lease.Close()
	return EntryFromManifest(lease.Snapshot())
}

func Load(ctx context.Context, store *checkpoint.Store, key Key) (*Document, error) {
	lease, err := store.AcquireCurrent(ctx, key.ID, key.Binding)
	if err != nil {
		return nil, err
	}
	m := lease.Snapshot()
	entry, err := EntryFromManifest(m)
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	d := &Document{Entry: entry, lease: lease, dataSize: m.DataBytes, value: make(map[Address]Value), templates: make(map[Address]string), rules: make(map[Area]Rule), receipts: make(map[string]receipt)}
	err = lease.VisitCandidate(ctx, func(b checkpoint.Block, r io.Reader) error {
		if b.Records != 1 || b.Bytes > 2*int64(formats.EditRequestBytes) {
			return checkpoint.ErrCorrupt
		}
		var tx transaction
		decoder := json.NewDecoder(r)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&tx) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF || !d.validTransaction(tx) {
			return checkpoint.ErrCorrupt
		}
		d.apply(tx, b.Sequence+1)
		return nil
	})
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	d.refreshInfo()
	return d, nil
}

func (d *Document) Close() error { return d.lease.Close() }

func (d *Document) ChangedSheets() map[string]bool {
	sheets := map[string]bool{}
	for a := range d.value {
		sheets[a.Sheet] = true
	}
	for a := range d.rules {
		sheets[a.Start.Sheet] = true
	}
	return sheets
}

func (d *Document) Snapshot() map[Address]Value { return maps.Clone(d.value) }

// ChangedAddresses iterates the current revision while the document is held.
func (d *Document) ChangedAddresses() iter.Seq[Address] { return maps.Keys(d.value) }

func (d *Document) ValueAt(address Address, base Value) Value {
	return d.value[address].Apply(base)
}

func (d *Document) refreshInfo() {
	d.Changed, d.Rules, d.Undo, d.Redo = len(d.value), len(d.rules), len(d.undo) != 0, len(d.redo) != 0
	d.Structures = len(d.workspaces)
}

func (d *Document) update(tx transaction, forward bool) {
	if tx.NextBase != nil {
		d.switchWorkspace(*tx.NextBase, forward)
		return
	}
	for _, p := range tx.Templates {
		raw := p.Before
		if forward {
			raw = p.After
		}
		if raw == nil {
			delete(d.templates, p.Address)
		} else {
			d.templates[p.Address] = *raw
		}
	}
	for _, p := range tx.Patches {
		v := p.Before
		if forward {
			v = p.After
		}
		if v == nil {
			delete(d.value, p.Address)
		} else {
			d.value[p.Address] = *v
		}
	}
	if p := tx.Rule; p != nil {
		r := p.Before
		if forward {
			r = p.After
		}
		if r == nil {
			delete(d.rules, p.Area)
		} else {
			d.rules[p.Area] = *r
		}
	}
}

func (d *Document) apply(tx transaction, revision uint64) {
	switch tx.Action {
	case "change", "rule", "structure":
		d.update(tx, true)
		d.undo = append(d.undo, tx)
		d.redo = nil
	case "undo":
		last := d.undo[len(d.undo)-1]
		d.undo = d.undo[:len(d.undo)-1]
		d.update(last, false)
		d.redo = append(d.redo, last)
	case "redo":
		last := d.redo[len(d.redo)-1]
		d.redo = d.redo[:len(d.redo)-1]
		d.update(last, true)
		d.undo = append(d.undo, last)
	}
	d.receipts[tx.Request] = receipt{tx.Digest, revision}
}

func ValidRequestID(id string) bool { return hexString(id, 16) }

func hexString(value string, n int) bool {
	if len(value) != 2*n {
		return false
	}
	data, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(data) == value
}

func validAddress(a Address) bool {
	return a.Row > 0 && a.Row <= formats.ExternalRows && a.Col > 0 && a.Col <= formats.ExcelColumns && len(a.Sheet) > 0 && len(a.Sheet) <= 512 && utf8.ValidString(a.Sheet)
}

func (d *Document) validTransaction(tx transaction) bool {
	if !ValidRequestID(tx.Request) || !hexString(tx.Digest, 32) || d.receipts[tx.Request].revision != 0 {
		return false
	}
	if tx.Action != "change" && len(tx.Templates) != 0 {
		return false
	}
	if (tx.NextBase != nil) != (tx.Action == "structure") {
		return false
	}
	switch tx.Action {
	case "structure":
		b := tx.NextBase
		return len(tx.Patches) == 0 && tx.Rule == nil && formats.EditOOXML(d.Base.Source.Format) &&
			b.Name == d.Base.Name && b.Source.Format == d.Base.Source.Format && b.Source.Path != "" &&
			b.Source.Book != d.Base.Source.Book && hexString(b.Source.Book, 32) &&
			b.Source.Stamp != "" && json.Valid(b.Layout)
	case "undo":
		return len(tx.Patches) == 0 && tx.Rule == nil && len(d.undo) > 0
	case "redo":
		return len(tx.Patches) == 0 && tx.Rule == nil && len(d.redo) > 0
	case "rule":
		p := tx.Rule
		if p == nil || !p.Area.Valid() || len(tx.Patches) != 0 {
			return false
		}
		original, exists := d.rules[p.Area]
		if exists != (p.Before != nil) || p.Before != nil && *p.Before != original || p.After == nil && !exists ||
			p.After != nil && (!p.After.Valid() || p.After.Area != p.Area || exists && *p.After == original) {
			return false
		}
		return exists || len(d.rules) < formats.EditValidationRules
	case "change":
		if tx.Rule != nil || len(tx.Patches)+len(tx.Templates) == 0 || len(tx.Patches) > formats.EditBatchCells || !d.validTemplates(tx.Templates) {
			return false
		}
	default:
		return false
	}
	count := len(d.value)
	seen := make(map[Address]bool, len(tx.Patches))
	for _, p := range tx.Patches {
		previous, found := d.value[p.Address]
		if !validAddress(p.Address) || seen[p.Address] || found != (p.Before != nil) || p.Before != nil && *p.Before != previous ||
			p.After != nil && !p.After.Valid() || p.Before == nil && p.After == nil || p.Before != nil && p.After != nil && *p.Before == *p.After {
			return false
		}
		seen[p.Address] = true
		if found {
			count--
		}
		if p.After != nil {
			count++
		}
	}
	return count <= formats.EditChangedCells
}

// HasReceipt reports whether Commit can resolve this request against history.
// Commit still checks the entire operation digest before returning the receipt.
func (d *Document) HasReceipt(request string) bool {
	_, found := d.receipts[request]
	return found
}

// Commit obtains base values or rule definitions only for a new operation.
// The file driver validates every target before any edit becomes durable.
func (d *Document) Commit(ctx context.Context, op Operation, readBase func([]Change) (map[Address]Value, error), readRule func(Rule) (*Rule, error)) (uint64, error) {
	var prepare func([]Change) (PreparedChanges, error)
	if readBase != nil {
		prepare = func(changes []Change) (PreparedChanges, error) {
			base, err := readBase(changes)
			return PreparedChanges{Changes: changes, Base: base}, err
		}
	}
	return d.CommitPlanned(ctx, op, prepare, readRule)
}

// CommitPlanned hashes the submitted operation before opening its source. Derived
// cells and column templates become part of the same durable undo step.
func (d *Document) CommitPlanned(ctx context.Context, op Operation, prepare func([]Change) (PreparedChanges, error), readRule func(Rule) (*Rule, error)) (uint64, error) {
	return d.commit(ctx, op, prepare, readRule, nil)
}

// CommitStructure materializes a verified revision only after checking the
// submitted request and its expected revision under the document lease.
func (d *Document) CommitStructure(ctx context.Context, op Operation, materialize func(Structure) (Base, error)) (uint64, error) {
	return d.commit(ctx, op, nil, nil, materialize)
}

func (d *Document) commit(ctx context.Context, op Operation, prepare func([]Change) (PreparedChanges, error), readRule func(Rule) (*Rule, error), materialize func(Structure) (Base, error)) (uint64, error) {
	if !ValidRequestID(op.Request) || len(op.Changes) > formats.EditBatchCells {
		return 0, ErrInvalid
	}
	encoded, err := json.Marshal(op)
	if err != nil || len(encoded) > formats.EditRequestBytes {
		return 0, ErrInvalid
	}
	hash := sha256.Sum256(encoded)
	tx := transaction{Request: op.Request, Digest: hex.EncodeToString(hash[:]), Action: op.Action}
	if past, ok := d.receipts[op.Request]; ok {
		if past.digest != tx.Digest {
			return 0, checkpoint.ErrConflict
		}
		return past.revision, nil
	}
	if op.Revision != d.Revision {
		return 0, checkpoint.ErrConflict
	}
	if len(d.receipts) >= formats.EditHistorySteps {
		return 0, ErrHistory
	}
	if (op.Structure != nil) != (op.Action == "structure") {
		return 0, ErrInvalid
	}
	switch op.Action {
	case "structure":
		if materialize == nil || len(op.Changes) != 0 || op.Rule != nil || !formats.EditOOXML(d.Base.Source.Format) {
			return 0, ErrInvalid
		}
		base, err := materialize(*op.Structure)
		if err != nil {
			return 0, err
		}
		tx.NextBase = &base
	case "change":
		if op.Rule != nil || prepare == nil || len(op.Changes) == 0 {
			return 0, ErrInvalid
		}
		seen := make(map[Address]bool, len(op.Changes))
		for _, c := range op.Changes {
			if !validAddress(c.Address) || !c.Value.Valid() || seen[c.Address] {
				return 0, ErrInvalid
			}
			seen[c.Address] = true
		}
		planned, err := prepare(op.Changes)
		if err != nil {
			return 0, err
		}
		if planned.NextBase != nil {
			// Keep the original request digest, including the submitted cells.
			// The existing snapshot transaction replays across old and new bases.
			tx.Action, tx.NextBase = "structure", planned.NextBase
			break
		}
		if len(planned.Changes) > formats.EditBatchCells {
			return 0, ErrHistory
		}
		for _, t := range planned.Templates {
			if t.Formula == d.ColumnFormula(t.Address, t.Original) {
				continue
			}
			p := templatePatch{Address: t.Address}
			if previous, ok := d.templates[t.Address]; ok {
				p.Before = &previous
			}
			if t.Formula != t.Original {
				p.After = &t.Formula
			}
			tx.Templates = append(tx.Templates, p)
		}
		for _, c := range planned.Changes {
			original, ok := planned.Base[c.Address]
			if !ok {
				return 0, ErrInvalid
			}
			current := d.ValueAt(c.Address, original)
			updated := c.Value.Apply(current)
			if current == updated {
				continue
			}
			p := patch{Address: c.Address}
			if before, ok := d.value[c.Address]; ok {
				p.Before = &before
			}
			if overlay := updated.difference(original); overlay != (Value{}) {
				p.After = &overlay
			}
			if p.Before != nil || p.After != nil {
				tx.Patches = append(tx.Patches, p)
			}
		}
		if len(tx.Patches)+len(tx.Templates) == 0 {
			return 0, ErrNoChange
		}
	case "rule":
		if op.Rule == nil || !op.Rule.Valid() || len(op.Changes) != 0 || readRule == nil {
			return 0, ErrInvalid
		}
		base, err := readRule(*op.Rule)
		if err != nil {
			return 0, err
		}
		before, exists := d.rules[op.Rule.Area]
		if exists && before == *op.Rule || !exists && base != nil && *base == *op.Rule {
			return 0, ErrNoChange
		}
		tx.Rule = &rulePatch{Area: op.Rule.Area}
		if exists {
			tx.Rule.Before = &before
		}
		if base == nil || *base != *op.Rule {
			tx.Rule.After = op.Rule
		}
	case "undo", "redo":
		if op.Rule != nil || len(op.Changes) != 0 {
			return 0, ErrInvalid
		}
	default:
		return 0, ErrInvalid
	}
	if !d.validTransaction(tx) {
		return 0, ErrHistory
	}
	payload, err := json.Marshal(tx)
	if err != nil || len(payload) > 2*formats.EditRequestBytes || d.dataSize+int64(len(payload))+128 > formats.EditHistoryBytes {
		return 0, ErrHistory
	}
	m, err := d.lease.Advance(ctx, checkpoint.Advance{
		Revision: d.Revision, Stage: "draft", Cursor: version, Bytes: int64(len(payload)), Records: 1,
	}, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	d.apply(tx, m.Revision)
	d.Revision, d.Updated, d.dataSize = m.Revision, m.Updated, m.DataBytes
	d.refreshInfo()
	return d.Revision, nil
}
