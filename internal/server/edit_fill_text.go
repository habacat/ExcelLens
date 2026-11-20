package server

import (
	"excellens/internal/editdoc"
	"math/big"
	"regexp"
	"strings"
)

var fillDigits = regexp.MustCompile(`(?s)^(.*?)([0-9]+)([^0-9]*)$`)
var fillLists = [][]string{
	strings.Fields("Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec"),
	strings.Fields("January February March April May June July August September October November December"),
	strings.Fields("Sun Mon Tue Wed Thu Fri Sat"),
	strings.Fields("Sunday Monday Tuesday Wednesday Thursday Friday Saturday"),
	strings.Fields("星期日 星期一 星期二 星期三 星期四 星期五 星期六"),
	strings.Fields("周日 周一 周二 周三 周四 周五 周六"),
	strings.Fields("1月 2月 3月 4月 5月 6月 7月 8月 9月 10月 11月 12月"),
}

type fillTextSeed struct {
	prefix, suffix, example string
	number                  *big.Int
	width                   int
	list                    []string
	group                   *fillTextGroup
}
type fillTextGroup struct {
	last, step *big.Int
	count      int
	consistent bool
}
type fillTextKey struct {
	prefix, suffix string
	list           int
}

func textFillSeeds(values []editdoc.Value) []fillTextSeed {
	seeds := make([]fillTextSeed, len(values))
	groups := map[fillTextKey]*fillTextGroup{}
	for i, v := range values {
		if v.Kind != "text" {
			continue
		}
		seed := fillTextSeed{example: v.Raw}
		key := fillTextKey{}
		for k, list := range fillLists {
			for pos, name := range list {
				if strings.EqualFold(name, v.Raw) {
					seed.number = big.NewInt(int64(pos))
					seed.list = list
					key.list = k + 1
					break
				}
			}
			if seed.list != nil {
				break
			}
		}
		if seed.number == nil {
			parts := fillDigits.FindStringSubmatch(v.Raw)
			if len(parts) == 0 || len(parts[2]) > 128 {
				continue
			}
			seed.prefix, seed.suffix = parts[1], parts[3]
			key.prefix, key.suffix = seed.prefix, seed.suffix
			seed.number, _ = new(big.Int).SetString(parts[2], 10)
			if len(parts[2]) > 1 && parts[2][0] == '0' {
				seed.width = len(parts[2])
			}
		}
		g := groups[key]
		if g == nil {
			g = &fillTextGroup{consistent: true, step: big.NewInt(1)}
			groups[key] = g
		}
		if g.count > 0 {
			delta := new(big.Int).Sub(seed.number, g.last)
			if g.count == 1 {
				g.step = delta
			} else if delta.Cmp(g.step) != 0 {
				g.consistent = false
			}
		}
		g.count++
		g.last = seed.number
		seed.group = g
		seeds[i] = seed
	}
	return seeds
}
func (s fillTextSeed) extend(cycle int) (string, bool) {
	if s.group == nil || !s.group.consistent {
		return "", false
	}
	offset := new(big.Int).Mul(s.group.step, big.NewInt(int64(cycle*s.group.count)))
	n := offset.Add(offset, s.number)
	if len(s.list) > 0 {
		result := s.list[n.Mod(n, big.NewInt(int64(len(s.list)))).Int64()]
		if strings.ToUpper(s.example) == s.example {
			return strings.ToUpper(result), true
		}
		if strings.ToLower(s.example) == s.example {
			return strings.ToLower(result), true
		}
		return result, true
	}
	digits := new(big.Int).Abs(n).String()
	sign := ""
	if n.Sign() < 0 {
		sign = "-"
	}
	return s.prefix + sign + strings.Repeat("0", max(0, s.width-len(digits))) + digits + s.suffix, true
}
