package analysis

import "testing"

func TestAccountingDecimalKeepsExactPrecisionAndRejectsMalformedNumbers(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"1,234.50", "2469/2"},
		{"(234.25)", "-937/4"},
		{"-1,000,000.000001", "-1000000000001/1000000"},
		{"12345678901234567890.123456", "192901232831790123283179/15625"},
		{"00000123", "123"}, // parser can read ID only on explicit conversion
	} {
		n, ok := AccountingDecimal(tc.in)
		if !ok || n.RatString() != tc.want {
			t.Errorf("%q got=%v ok=%v, want %s", tc.in, n, ok, tc.want)
		}
	}
	for _, invalid := range []string{"1,23", "1,234,56", "1,234.2,5", "(123", "ABC", "(1,2)", "0,001,234"} {
		if _, ok := AccountingDecimal(invalid); ok {
			t.Fatalf("invalid accounting amount %q accepted", invalid)
		}
	}
	if IsAccountingNumericLiteral("0000123") || IsAccountingNumericLiteral("001,234") || !IsAccountingNumericLiteral("(2,345.50)") {
		t.Fatal("finance ID recognition incorrect")
	}
	if _, ok := Decimal("1,234.50"); ok {
		t.Fatal("ordinary Decimal grammar changed (should remain strict)")
	}
}
