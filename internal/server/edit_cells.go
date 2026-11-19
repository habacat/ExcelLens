package server

import (
	"context"
	"errors"
	"fmt"

	"excellens/internal/checkpoint"
	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/workbook"
)

func (a *App) editRead(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, req editRequest) (any, error) {
	if req.Revision != doc.Revision {
		return nil, checkpoint.ErrConflict
	}
	col, row, err := workbook.ParseCellRef(req.Ref)
	if err != nil {
		return nil, editfile.ErrTarget
	}
	f, err := openEditBase(ctx, doc)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	address := editdoc.Address{Sheet: req.Sheet, Row: row, Col: col}
	values, err := layout.Read(ctx, f, []editdoc.Address{address})
	if err != nil {
		return nil, err
	}
	source := values[address]
	current := doc.ValueAt(address, source.Value)
	var formulaState string
	var savedResult, calculatedResult *editdoc.Value
	calc, err := a.calculationFor(ctx, doc, layout, f)
	if err != nil {
		return nil, err
	}
	if current.Kind == "formula" {
		formulaState, savedResult = calc.FormulaResult(address, source)
		if formulaState == "calculated" {
			calculatedResult, savedResult = savedResult, nil
		}
	}
	status := calc.GeneratedStatus(address)
	rule := editRuleAt(doc, layout, address)
	if rule == nil {
		rule = source.RuleAt(address)
	}
	var choices []string
	if rule != nil && !rule.Reference {
		choices = editfile.RuleChoices(*rule)
	}
	return struct {
		Address          editdoc.Address `json:"address"`
		Ref              string          `json:"ref"`
		Base             editdoc.Value   `json:"base"`
		Value            editdoc.Value   `json:"value"`
		FormulaState     string          `json:"formulaState,omitempty"`
		SavedResult      *editdoc.Value  `json:"savedResult,omitempty"`
		CalculatedResult *editdoc.Value  `json:"calculatedResult,omitempty"`
		Rule             *editdoc.Rule   `json:"rule,omitempty"`
		Choices          []string        `json:"choices,omitempty"`
		ReadOnly         string          `json:"readOnly,omitempty"`
		GeneratedStatus  string          `json:"generatedStatus,omitempty"`
	}{
		Address: address, Ref: fmt.Sprintf("%s%d", workbook.ColumnName(col), row), Base: source.Value, Value: current,
		FormulaState: formulaState, SavedResult: savedResult, CalculatedResult: calculatedResult, Rule: rule, Choices: choices, ReadOnly: source.ReadOnly, GeneratedStatus: status,
	}, nil
}

func editRuleAt(doc *editdoc.Document, layout editfile.Layout, address editdoc.Address) *editdoc.Rule {
	for _, rule := range layout.EffectiveRules(doc.RuleChanges()) {
		if rule.Contains(address) {
			return &rule
		}
	}
	return nil
}

func editChoices(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, req editRequest) (any, error) {
	if req.Revision != doc.Revision {
		return nil, checkpoint.ErrConflict
	}
	address, err := editAddress(req.Sheet, req.Ref)
	if err != nil || !layout.Contains(address) {
		return nil, editfile.ErrTarget
	}
	f, err := openEditBase(ctx, doc)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	batch, err := layout.ReadBatch(ctx, f, []editdoc.Address{address}, doc.RuleChanges())
	if err != nil {
		return nil, err
	}
	choices, err := batch.ListChoices(address, doc)
	if err != nil {
		return nil, err
	}
	return struct {
		Revision uint64   `json:"revision,string"`
		Choices  []string `json:"choices"`
	}{doc.Revision, choices}, nil
}

func editChangeAddresses(changes []editdoc.Change) []editdoc.Address {
	addresses := make([]editdoc.Address, len(changes))
	for i, change := range changes {
		addresses[i] = change.Address
	}
	return addresses
}

func (a *App) editCommit(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, req editRequest) (any, error) {
	op := req.Operation
	if op.Action == "structure" {
		committed, err := commitEditStructure(ctx, doc, layout, op)
		if err != nil {
			return nil, err
		}
		return editCommitResult{doc.Info, committed, true, doc.Base.Source.Book}, nil
	}
	var f *workbook.EditSourceFile
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()
	if (op.Action == "undo" || op.Action == "redo") && !doc.HasReceipt(op.Request) {
		var err error
		f, err = openEditBase(ctx, doc)
		if err != nil {
			return nil, err
		}
	}
	if next := doc.HistoryBase(op.Action); next != nil && !doc.HasReceipt(op.Request) && op.Revision == doc.Revision {
		file, err := openVerifiedEditSource(ctx, *next)
		if err != nil {
			return nil, err
		}
		defer file.Close()
	}
	commit, err := doc.CommitPlanned(ctx, op, func(changes []editdoc.Change) (editdoc.PreparedChanges, error) {
		if err := a.checkEditStyleProof(doc, req.Proof, changes); err != nil {
			return editdoc.PreparedChanges{}, err
		}
		var err error
		f, err = openEditBase(ctx, doc)
		if err != nil {
			return editdoc.PreparedChanges{}, err
		}
		layout, err = a.documentEditLayout(ctx, doc, layout, f)
		if err != nil {
			return editdoc.PreparedChanges{}, err
		}
		return layout.PrepareChanges(ctx, f, changes, doc)
	}, func(rule editdoc.Rule) (*editdoc.Rule, error) {
		if !a.validEditProof(doc, req.Proof, rule) {
			return nil, errors.New("请重新预览当前修订的数据规则")
		}
		var err error
		f, err = openEditBase(ctx, doc)
		if err != nil {
			return nil, err
		}
		layout, err = a.documentEditLayout(ctx, doc, layout, f)
		if err != nil {
			return nil, err
		}
		overlays := doc.RuleChanges()
		base, err := layout.RuleBase(rule, overlays)
		if err != nil {
			return nil, err
		}
		return base, layout.CheckRuleReference(ctx, f, rule, overlays, doc.ValueAt)
	})
	if err != nil {
		return nil, err
	}
	return editCommitResult{doc.Info, commit, layout.XLSX != nil && len(layout.XLSX.Tables) > 0, doc.Base.Source.Book}, nil
}

type editCommitResult struct {
	Document   editdoc.Info `json:"document"`
	Committed  uint64       `json:"committed,string"`
	ReloadGrid bool         `json:"reloadGrid,omitempty"`
	Book       string       `json:"book"`
}
