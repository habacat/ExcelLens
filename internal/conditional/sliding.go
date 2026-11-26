package conditional

import (
	"errors"
	"math/big"

	"excellens/internal/excelformula"
)

type slidingKey struct {
	node *Expression[EvalValue]
	col  int
}

type slidingTerm struct {
	rat    *big.Rat
	fault  string
	notice string
	shift  int // -1 for a non-dyadic source number
}

type slidingWindow struct {
	col       int
	lo, hi    int
	valid     bool
	nonDyadic int
	shifts    map[int]int
	sum       *big.Rat
	absSum    *big.Rat
	denBits   int
	issueRows int
	items     map[int]slidingTerm
}

// evalSlidingSum maintains an incremental sliding-window SUM for short/medium
// moving vertical ranges without building a full-sheet disk prefix-sum index.
func (e *Engine) evalSlidingSum(node *Expression[EvalValue], col, lo, hi int) (EvalValue, string, error, bool) {
	if e.Resolver == nil {
		return EvalValue{}, "", nil, false
	}
	if e.sliding == nil {
		e.sliding = make(map[slidingKey]*slidingWindow)
	}
	key := slidingKey{node: node, col: col}
	w := e.sliding[key]
	if w == nil {
		w = &slidingWindow{
			col:    col,
			sum:    new(big.Rat),
			absSum: new(big.Rat),
			items:  make(map[int]slidingTerm),
		}
		e.sliding[key] = w
	}

	readRow := func(row int) (slidingTerm, error) {
		if err := e.Ctx.Err(); err != nil {
			return slidingTerm{}, err
		}
		if !e.Spend(1) {
			return slidingTerm{notice: WorkNotice}, nil
		}
		var v EvalValue
		var notice string
		if row == e.Row && col == e.Col {
			v, notice = e.CurrentValue()
		} else {
			cell, available, err := e.Resolver(row, col)
			if err != nil {
				var unavailable *DependencyUnavailableError
				if errors.As(err, &unavailable) {
					return slidingTerm{notice: unavailable.Reason}, nil
				}
				return slidingTerm{}, err
			}
			if !available {
				return slidingTerm{notice: "引用的源单元格尚未读取，不能用空白或零替代"}, nil
			}
			v, notice = e.CellValue(cell)
		}
		if notice != "" {
			return slidingTerm{notice: notice}, nil
		}
		if v.Kind == "error" {
			return slidingTerm{fault: v.Text}, nil
		}
		if v.Kind == "number" {
			r, ok := ValueToRat(v)
			if !ok {
				return slidingTerm{fault: "#NUM!"}, nil
			}
			den := r.Denom()
			shift := den.BitLen() - 1
			if int(den.TrailingZeroBits()) != shift {
				shift = -1
			}
			return slidingTerm{rat: r, shift: shift}, nil
		}
		return slidingTerm{}, nil
	}

	addTerm := func(row int, t slidingTerm) {
		if t.notice != "" || t.fault != "" {
			w.items[row] = t
			w.issueRows++
			return
		}

		if t.rat == nil || t.rat.Sign() == 0 {
			return
		}
		w.items[row] = t
		if t.shift < 0 {
			w.nonDyadic++
			return
		}
		if t.shift > 0 {
			if w.shifts == nil {
				w.shifts = make(map[int]int)
			}
			w.shifts[t.shift]++
			w.denBits = max(w.denBits, t.shift)
		}
		w.sum.Add(w.sum, t.rat)
		if t.rat.Sign() > 0 {
			w.absSum.Add(w.absSum, t.rat)
		} else {
			w.absSum.Sub(w.absSum, t.rat)
		}
	}

	removeRow := func(row int) {
		t, ok := w.items[row]
		if !ok {
			return
		}
		delete(w.items, row)
		if t.notice != "" || t.fault != "" {
			w.issueRows--
			return
		}

		if t.shift < 0 {
			w.nonDyadic--
			return
		}
		w.sum.Sub(w.sum, t.rat)
		if t.rat.Sign() > 0 {
			w.absSum.Sub(w.absSum, t.rat)
		} else {
			w.absSum.Add(w.absSum, t.rat)
		}
		if t.shift == 0 {
			return
		}
		w.shifts[t.shift]--
		if w.shifts[t.shift] == 0 {
			delete(w.shifts, t.shift)
			if t.shift == w.denBits {
				w.denBits = 0
				for shift := range w.shifts {
					w.denBits = max(w.denBits, shift)
				}
			}
		}
	}

	reset := func() {
		clear(w.items)
		w.valid = false
		w.nonDyadic = 0
		clear(w.shifts)
		w.sum.SetInt64(0)
		w.absSum.SetInt64(0)
		w.denBits = 0
		w.issueRows = 0
	}

	if !w.valid || w.col != col || hi < w.lo || lo > w.hi {
		reset()
		w.col = col
		for r := lo; r <= hi; r++ {
			t, err := readRow(r)
			if err != nil {
				reset()
				return EvalValue{}, "", err, true
			}
			if t.notice != "" {
				// Keep the failure honest without caching a partial/unreadable window.
				reset()
				return EvalValue{}, t.notice, nil, true
			}
			addTerm(r, t)
		}
		w.lo, w.hi, w.valid = lo, hi, true
	} else {
		// Remove rows leaving the window.
		if lo > w.lo {
			for r := w.lo; r < lo; r++ {
				removeRow(r)
			}
		}
		if hi < w.hi {
			for r := hi + 1; r <= w.hi; r++ {
				removeRow(r)
			}
		}
		// Fetch only rows entering the window.
		if lo < w.lo {
			for r := lo; r < w.lo; r++ {
				t, err := readRow(r)
				if err != nil {
					reset()
					return EvalValue{}, "", err, true
				}
				if t.notice != "" {
					reset()
					return EvalValue{}, t.notice, nil, true
				}
				addTerm(r, t)
			}
		}
		if hi > w.hi {
			for r := w.hi + 1; r <= hi; r++ {
				t, err := readRow(r)
				if err != nil {
					reset()
					return EvalValue{}, "", err, true
				}
				if t.notice != "" {
					reset()
					return EvalValue{}, t.notice, nil, true
				}
				addTerm(r, t)
			}
		}
		w.lo, w.hi = lo, hi
	}

	if w.issueRows > 0 {
		for r := lo; r <= hi; r++ {
			if t, ok := w.items[r]; ok {
				if t.notice != "" {
					return EvalValue{}, t.notice, nil, true
				}
				if t.fault != "" {
					return ErrorValue(t.fault), "", nil, true
				}
			}
		}
	}
	if w.nonDyadic == 0 && w.absSum.Num().BitLen()+w.denBits-(w.absSum.Denom().BitLen()-1) <= 53 {
		return SumResult(w.sum), "", nil, true
	}
	// Non-dyadic or >53-bit window: replay occupied terms in exact source order
	// from the in-memory sliding cache without re-reading disk rows.
	acc := new(big.Rat)
	for r := lo; r <= hi; r++ {
		t, ok := w.items[r]
		if !ok || t.rat == nil {
			continue
		}
		if !e.Spend(1) {
			return EvalValue{}, WorkNotice, nil, true
		}
		if !excelformula.AddFloat(acc, t.rat) {
			return ErrorValue("#NUM!"), "", nil, true
		}
	}
	return SumResult(acc), "", nil, true
}
