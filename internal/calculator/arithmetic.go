package calculator

import (
	"errors"
	"excellens/internal/analysis"
	"math/big"
)

func accountingNumber(s string) (*big.Rat, bool) { return analysis.AccountingDecimal(s) }
func binaryArithmetic(op byte, a, b *big.Rat) (*big.Rat, error) {
	v := new(big.Rat).Set(a)
	switch op {
	case '+':
		v.Add(v, b)
	case '-':
		v.Sub(v, b)
	case '*':
		v.Mul(v, b)
	case '/':
		if b.Sign() == 0 {
			return nil, errors.New("#DIV/0! 除数不能为0")
		}
		v.Quo(v, b)
	case '^':
		if !b.IsInt() || !b.Num().IsInt64() || b.Num().Int64() < -100000 || b.Num().Int64() > 100000 {
			return nil, errors.New("#NUM! 整数幂次超出范围")
		}
		n := b.Num().Int64()
		if n == 0 && v.Sign() == 0 {
			return nil, errors.New("#NUM! 0的0次幂无定义")
		}
		if int64(max(v.Num().BitLen(), v.Denom().BitLen())-1)*abs(n) > maxBits {
			return nil, errors.New("#NUM! 幂运算结果过大")
		}
		if v.Sign() == 0 && n < 0 {
			return nil, errors.New("#DIV/0! 0不能取负次幂")
		}
		num := new(big.Int).Exp(v.Num(), big.NewInt(abs(n)), nil)
		den := new(big.Int).Exp(v.Denom(), big.NewInt(abs(n)), nil)
		if n < 0 {
			num, den = den, num
		}
		v.SetFrac(num, den)
	default:
		return nil, errors.New("#VALUE! 运算符无效")
	}
	if err := bounded(v); err != nil {
		return nil, err
	}
	return v, nil
}
