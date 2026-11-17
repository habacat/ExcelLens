package printing

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

func measure(text string, roundUp bool) (int64, error) {
	text = strings.TrimSpace(text)
	if len(text) == 0 || len(text) > formats.PrintGeometryMeasureBytes {
		return 0, errors.New("纸张或边距尺寸为空或超长")
	}
	multiplier := big.NewRat(1, 1)
	for _, unit := range []struct {
		suffix string
		n, d   int64
	}{{"mm", 360, 127}, {"cm", 3600, 127}, {"in", 72, 1}, {"pt", 1, 1}, {"pc", 12, 1}, {"pi", 12, 1}} {
		if strings.HasSuffix(text, unit.suffix) {
			text = strings.TrimSpace(strings.TrimSuffix(text, unit.suffix))
			multiplier = big.NewRat(unit.n, unit.d)
			break
		}
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || strings.ContainsAny(text, "xXpP/_") {
		return 0, errors.New("纸张或边距尺寸无效")
	}
	if at := strings.IndexAny(text, "eE"); at >= 0 {
		exponent, err := strconv.Atoi(text[at+1:])
		if err != nil || exponent < -400 || exponent > 400 {
			return 0, errors.New("纸张或边距尺寸指数超过容量")
		}
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok || value.Sign() < 0 {
		return 0, errors.New("纸张或边距不是有效十进制")
	}
	value.Mul(value, multiplier)
	value.Mul(value, big.NewRat(formats.PrintGeometryUnitsPerPoint, 1))
	integer, remainder := new(big.Int), new(big.Int)
	integer.QuoRem(value.Num(), value.Denom(), remainder)
	if roundUp && remainder.Sign() != 0 {
		integer.Add(integer, big.NewInt(1))
	}
	if !integer.IsInt64() {
		return 0, errors.New("纸张或边距尺寸超过计算容量")
	}
	return integer.Int64(), nil
}

func scaled(units int64, scale *big.Rat) (int64, error) {
	value := new(big.Rat).Mul(big.NewRat(units, 1), scale)
	integer := new(big.Int).Quo(value.Num(), value.Denom())
	if !integer.IsInt64() {
		return 0, errors.New("缩放后的打印长度超过容量")
	}
	return integer.Int64(), nil
}
func unscaled(units int64, scale *big.Rat) (int64, error) {
	return scaled(units, new(big.Rat).Inv(scale))
}
func add(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, errors.New("打印长度合计超过容量")
	}
	return a + b, nil
}
