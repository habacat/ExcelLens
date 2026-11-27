package analysis

import "testing"

func BenchmarkDecimalLedger(b *testing.B) {
	inputs := []string{"1250.75", "-0.0001", "(1,234.56)", "9007199254740993.125", "12e-12", "1/7", "000023", "未过账"}
	b.ReportAllocs()
	for b.Loop() {
		for i, input := range inputs {
			_, ok := AccountingNumericValue(input)
			if ok != (i < 6) {
				b.Fatal(input, ok)
			}
		}
	}
}
