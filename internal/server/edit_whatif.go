package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"strings"

	"excellens/internal/analysis"
	"excellens/internal/checkpoint"
	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/formats"
)

type whatIfSpec struct {
	Mode                          string `json:"mode"`
	Input, Second, Target         editdoc.Address
	Goal, Lower, Upper, Tolerance string
	Values, SecondValues          []string
}
type trialPoint struct {
	Input  string `json:"input"`
	Second string `json:"second,omitempty"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}
type whatIfResult struct {
	Status     string       `json:"status"`
	Iterations int          `json:"iterations"`
	Residual   string       `json:"residual,omitempty"`
	Points     []trialPoint `json:"points"`
}

func trialNumber(raw string) (*big.Rat, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > formats.TrialDigits || strings.Contains(raw, "/") {
		return nil, errors.New("请输入有效的有限十进制数")
	}
	v, ok := analysis.Decimal(raw)
	if !ok || v.Num().BitLen() > 1024 || v.Denom().BitLen() > 1024 {
		return nil, errors.New("试算数值无效或超出精度范围")
	}
	return v, nil
}
func (a *App) editWhatIf(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, req editRequest) (any, error) {
	if req.Revision != doc.Revision || req.Book != doc.Base.Source.Book || a.currentWorkbook() == nil || sourceBookID(a.currentWorkbook()) != req.Book {
		return nil, checkpoint.ErrConflict
	}
	s := req.Trial
	if s == nil || layout.XLSX == nil {
		return nil, errors.New("试算需要已确认的工作簿编辑修订")
	}
	if s.Mode != "seek" && s.Mode != "table" {
		return nil, errors.New("试算方式无效")
	}
	if s.Input == s.Target || s.Input == s.Second || s.Second == s.Target {
		return nil, errors.New("输入格与公式目标格须各不相同")
	}
	f, err := openEditBase(ctx, doc)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	addresses := []editdoc.Address{s.Input, s.Target}
	second := s.Second != (editdoc.Address{})
	if second {
		if s.Mode != "table" || len(s.SecondValues) == 0 {
			return nil, errors.New("第二个输入格只用于双变量试算")
		}
		addresses = append(addresses, s.Second)
	} else if len(s.SecondValues) > 0 {
		return nil, errors.New("未指定第二个输入格")
	}
	for _, addr := range addresses {
		if !layout.XLSX.HasSheet(addr.Sheet) || addr.Col < 1 || addr.Col > formats.ExcelColumns || addr.Row < 1 || addr.Row > formats.ExcelRows {
			return nil, errors.New("试算源地址无效")
		}
	}
	cells, err := layout.Read(ctx, f, addresses)
	if err != nil {
		return nil, err
	}
	entry, err := a.edits.calculationRevision(ctx, doc, layout, f, true)
	if err != nil {
		return nil, err
	}
	if entry.book == nil || entry.indexErr != nil {
		return nil, errors.New("此工作簿的计算设置或规模超出本机试算能力")
	}
	patches := maps.Clone(entry.patches)
	if patches == nil {
		patches = make(map[editdoc.Address]editdoc.Value)
	}
	target := patches[s.Target].Apply(cells[s.Target].Value)
	if target.Kind != "formula" {
		return nil, errors.New("目标单元格必须是公式")
	}
	for _, input := range append(addresses[:1:1], addresses[2:]...) {
		source := cells[input]
		actual := patches[input].Apply(source.Value)
		if actual.Kind != "number" || source.Array != nil {
			return nil, errors.New("输入格必须是独立的数值常量")
		}
	}
	// Mark the target as an explicit formula, even when neither input affects
	// it. Every point is evaluated; a saved source cache is never a trial result.
	patches[s.Target] = target
	evaluate := func(x, y string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		patches[s.Input] = editdoc.Value{Kind: "number", Raw: x}
		if second {
			patches[s.Second] = editdoc.Value{Kind: "number", Raw: y}
		}
		result, err := entry.book.Calculate(ctx, patches, nil)
		if err != nil {
			return "", err
		}
		cell, ok := result.Cells[s.Target]
		if ok && cell.State == "cycle" {
			return "", errors.New("目标公式含循环引用")
		}
		if !ok || cell.State != "calculated" {
			return "", errors.New("目标公式或依赖尚不支持本机重算")
		}
		if cell.Value.Kind != "number" {
			return "", fmt.Errorf("目标没有数值结果：%s", cell.Value.Raw)
		}
		return cell.Value.Raw, nil
	}
	var result whatIfResult
	if s.Mode == "seek" {
		result, err = seekTrial(ctx, *s, evaluate)
	} else {
		result, err = tableTrial(ctx, *s, evaluate)
	}
	if err == nil {
		err = f.Check()
	}
	return result, err
}

func tableTrial(ctx context.Context, s whatIfSpec, evaluate func(string, string) (string, error)) (whatIfResult, error) {
	out := whatIfResult{Status: "完成", Points: []trialPoint{}}
	ys := s.SecondValues
	if len(ys) == 0 {
		ys = []string{""}
	}
	if len(s.Values) == 0 || len(ys) > formats.TrialPoints || len(s.Values) > formats.TrialPoints/len(ys) {
		return out, errors.New("试算组合为空或超过 256 组")
	}
	for _, v := range append(append([]string{}, s.Values...), s.SecondValues...) {
		if _, err := trialNumber(v); err != nil {
			return out, err
		}
	}
	for _, x := range s.Values {
		for _, y := range ys {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			value, err := evaluate(x, y)
			point := trialPoint{Input: x, Second: y, Result: value}
			if err != nil {
				point.Error = err.Error()
				out.Status = "部分组合没有数值结果"
			}
			out.Points = append(out.Points, point)
			out.Iterations++
		}
	}
	return out, ctx.Err()
}

func seekTrial(ctx context.Context, s whatIfSpec, evaluate func(string, string) (string, error)) (whatIfResult, error) {
	out := whatIfResult{Points: []trialPoint{}}
	goal, err := trialNumber(s.Goal)
	if err != nil {
		return out, err
	}
	lo, err := trialNumber(s.Lower)
	if err != nil {
		return out, err
	}
	hi, err := trialNumber(s.Upper)
	if err != nil {
		return out, err
	}
	tol, err := trialNumber(s.Tolerance)
	if err != nil || tol.Sign() <= 0 || hi.Cmp(lo) <= 0 {
		return out, errors.New("上界须大于下界，允许残差须为正数")
	}
	var best *big.Rat
	probe := func(x *big.Rat) (*big.Rat, bool, error) {
		input := analysis.ExactDecimalString(x)
		value, err := evaluate(input, "")
		out.Iterations++
		if err != nil {
			return nil, false, err
		}
		number, ok := analysis.Decimal(value)
		if !ok {
			return nil, false, errors.New("公式结果无法精确比较")
		}
		res := new(big.Rat).Sub(number, goal)
		distance := new(big.Rat).Abs(res)
		if best == nil || distance.Cmp(best) < 0 {
			best = distance
			out.Residual = analysis.ExactDecimalString(res)
			out.Points = []trialPoint{{Input: input, Result: value}}
		}
		return res, distance.Cmp(tol) <= 0, nil
	}
	l, done, err := probe(lo)
	if err != nil {
		return out, err
	}
	if done {
		out.Status = "达到目标"
		return out, nil
	}
	h, done, err := probe(hi)
	if err != nil {
		return out, err
	}
	if done {
		out.Status = "达到目标"
		return out, nil
	}
	if l.Sign() == h.Sign() {
		out.Status = "区间两端未夹住目标"
		return out, nil
	}
	for out.Iterations < formats.TrialIterations {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		mid := new(big.Rat).Quo(new(big.Rat).Add(lo, hi), big.NewRat(2, 1))
		res, done, err := probe(mid)
		if err != nil {
			return out, err
		}
		if done {
			out.Status = "达到目标"
			return out, nil
		}
		if res.Sign() == l.Sign() {
			lo = mid
			l = res
		} else {
			hi = mid
		}
	}
	out.Status = "达到迭代上限，尚未满足残差"
	return out, nil
}
