package store

import "testing"

func TestPointPriceDiscountRates(t *testing.T) {
	for _, tc := range []struct {
		percent int
		want    string
	}{{0, "100.000000"}, {10, "90.000000"}, {20, "80.000000"}, {99, "1.000000"}} {
		p := PointPrice{Input: "100", Output: "100", CacheRead: "0", CacheWrite: "100", DiscountPercent: tc.percent, StandardPrice: &PointPriceRates{"100", "100", "0", "100"}}
		got, err := NormalizePointPrice(p)
		if err != nil || got.Input != tc.want || got.Output != tc.want || got.CacheWrite != tc.want || got.CacheRead != "0.000000" {
			t.Fatalf("discount %d: %#v, %v", tc.percent, got, err)
		}
		if tc.percent == 0 && got.StandardPrice != nil {
			t.Fatal("undiscounted price retained a stale reference")
		}
		if p.StandardPrice.Input != "100" {
			t.Fatal("normalization changed the caller's reference price")
		}
	}
	precise, err := NormalizePointPrice(PointPrice{DiscountPercent: 10, StandardPrice: &PointPriceRates{"99999999999999.999999", "0.123456", "0", "0.000001"}})
	if err != nil || precise.Input != "89999999999999.999999" || precise.Output != "0.111110" || precise.CacheWrite != "0.000001" {
		t.Fatalf("fixed precision: %#v %v", precise, err)
	}
}

func TestPointPriceRejectsInvalidDiscounts(t *testing.T) {
	for _, p := range []PointPrice{
		{DiscountPercent: -1}, {DiscountPercent: 100}, {DiscountPercent: 10},
		{Input: "bad"},
		{DiscountPercent: 10, StandardPrice: &PointPriceRates{"-1", "1", "0", "0"}},
		{DiscountPercent: 10, StandardPrice: &PointPriceRates{"1", "1.1234567", "0", "0"}},
		{DiscountPercent: 99, StandardPrice: &PointPriceRates{"0.000001", "1", "0", "0"}},
	} {
		if _, err := NormalizePointPrice(p); err == nil {
			t.Fatalf("accepted invalid offer %#v", p)
		}
	}
}
