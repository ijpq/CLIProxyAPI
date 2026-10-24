package billingvalue

import "testing"

func TestDecimalValidationAndTokenPrices(t *testing.T) {
	for _, raw := range []string{"NaN", "Inf", "-1", "1e3", "0.0000001", "100000000000000", "01.0", ""} {
		if _, err := Normalize(raw, false); err == nil {
			t.Errorf("invalid decimal accepted: %q", raw)
		}
	}
	if _, err := Normalize("0", true); err == nil {
		t.Fatal("zero exchange rate accepted")
	}
	if got, err := Normalize(" 12.30 ", true); err != nil || got != "12.300000" {
		t.Fatalf("normalize=%s err=%v", got, err)
	}
	got, err := TokenCost([]string{"10", "20", "0", "1"}, []int64{1_000_000, 500_000, 900_000, 10_000})
	if err != nil || got != "20.010000" {
		t.Fatalf("points cost=%s err=%v", got, err)
	}
	if got, err = TokenCost([]string{"0.000001"}, []int64{500_000}); err != nil || got != "0.000001" {
		t.Fatalf("half-point precision=%s err=%v", got, err)
	}
	if _, err = TokenCost([]string{"99999999999999"}, []int64{9_000_000}); err == nil {
		t.Fatal("overflow charge accepted")
	}
}
