package okx

import (
	"testing"
	"time"
)

// newQtyResolveTrader gives a trader with cached instrument specs only. No HTTP
// is needed: every function under test reads the cache.
func newQtyResolveTrader() *OKXTrader {
	tr := &OKXTrader{
		instrumentsCache: map[string]*OKXInstrument{
			// Real OKX specs as of 2026-08-01.
			"ZEC-USDT-SWAP": {InstID: "ZEC-USDT-SWAP", CtVal: 0.01, CtMult: 1, LotSz: 1, MinSz: 1, TickSz: 0.01, CtType: "linear"},
			"UNI-USDT-SWAP": {InstID: "UNI-USDT-SWAP", CtVal: 1, CtMult: 1, LotSz: 1, MinSz: 1, TickSz: 0.001, CtType: "linear"},
			"ETH-USDT-SWAP": {InstID: "ETH-USDT-SWAP", CtVal: 0.1, CtMult: 1, LotSz: 0.01, MinSz: 0.01, TickSz: 0.01, CtType: "linear"},
			// Synthetic: no live OKX USDT swap has LotSz > MinSz (421/421 equal on
			// 2026-08-01), but the validator must still refuse what the wire would.
			"LOT-USDT-SWAP": {InstID: "LOT-USDT-SWAP", CtVal: 1, CtMult: 1, LotSz: 5, MinSz: 1, TickSz: 0.01, CtType: "linear"},
		},
		cachedOpenOrders: map[string]cachedOpenOrderEntry{},
	}
	tr.instrumentsCacheTime = time.Now()
	return tr
}

// TestProtectionSizeClampsUpToMinSz pins the exact behaviour the validator now
// relies on: the placement path rounds UP to MinSz rather than rounding to
// nearest. The ZEC row is the live 2026-08-01 case — 0.99305 contracts, short of
// MinSz 1 by 0.007 — which used to be discarded before reaching the wire.
func TestProtectionSizeClampsUpToMinSz(t *testing.T) {
	tr := newQtyResolveTrader()
	inst := tr.instrumentsCache["ZEC-USDT-SWAP"]

	cases := []struct {
		name     string
		qty      float64
		wantSz   float64
		wantWire string
	}{
		{"live 12% tier just under min", 0.0099305275, 1, "1"},
		{"far under min still clamps", 0.001, 1, "1"},
		{"exactly min", 0.01, 1, "1"},
		{"above min rounds to nearest", 0.0165, 2, "2"},
		{"above min rounds down", 0.0149, 1, "1"},
	}
	for _, c := range cases {
		got, wire, err := tr.protectionSizeForQuantity(inst, c.qty)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", c.name, err)
		}
		if got != c.wantSz || wire != c.wantWire {
			t.Fatalf("%s: qty=%.10f → %v/%q, want %v/%q", c.name, c.qty, got, wire, c.wantSz, c.wantWire)
		}
	}
}

// TestValidateProtectionQuantityAcceptsClampable is the regression for the
// dropped-tier defect: a tier below MinSz pre-clamping must validate, because
// the placement path clamps it up and the venue accepts it.
func TestValidateProtectionQuantityAcceptsClampable(t *testing.T) {
	tr := newQtyResolveTrader()
	// ZEC 12% tier of an 8.276-contract position.
	if err := tr.ValidateProtectionQuantity("ZECUSDT", 0.0099305275); err != nil {
		t.Fatalf("ZEC 12%% tier must validate (placement clamps to MinSz), got %v", err)
	}
	// UNI 12% tier of a 7-contract position = 0.84 contracts.
	if err := tr.ValidateProtectionQuantity("UNIUSDT", 0.84); err != nil {
		t.Fatalf("UNI 12%% tier must validate, got %v", err)
	}
	// ETH has a fine minimum; a normal tier is unaffected.
	if err := tr.ValidateProtectionQuantity("ETHUSDT", 0.0234); err != nil {
		t.Fatalf("ETH tier must validate, got %v", err)
	}
}

// TestValidateProtectionQuantityRejectsImpossible keeps the guard meaningful.
func TestValidateProtectionQuantityRejectsImpossible(t *testing.T) {
	tr := newQtyResolveTrader()
	for _, qty := range []float64{0, -1} {
		if err := tr.ValidateProtectionQuantity("ZECUSDT", qty); err == nil {
			t.Fatalf("qty=%v must be rejected", qty)
		}
	}
	// An unknown symbol is deliberately NOT asserted here: a cache miss makes
	// getInstrument fetch from the venue, which needs network. The error path is
	// unchanged by this work anyway.
	//
	// LotSz > MinSz: clamping only reaches MinSz=1, still below LotSz=5, so the
	// wire would reject it and so must we.
	if err := tr.ValidateProtectionQuantity("LOTUSDT", 0.5); err == nil {
		t.Fatalf("resolved size below LotSz must be rejected")
	}
}

// TestProtectionContractsForQuantity is what the caller's oversell guard reads.
func TestProtectionContractsForQuantity(t *testing.T) {
	tr := newQtyResolveTrader()
	// Position 0.08276 ZEC = 8.276 contracts → rounds to 8 on the wire.
	pos, err := tr.ProtectionContractsForQuantity("ZECUSDT", 0.08276)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if pos != 8 {
		t.Fatalf("position contracts expected 8, got %v", pos)
	}
	// The four ladder tiers of that position: 20/18/15/12%.
	var total float64
	for _, pct := range []float64{20, 18, 15, 12} {
		c, err := tr.ProtectionContractsForQuantity("ZECUSDT", 0.08276*pct/100.0)
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		total += c
	}
	// 1.655→2, 1.490→1, 1.241→1, 0.993→1 (clamped) = 5 ≤ 8, no oversell.
	if total != 5 {
		t.Fatalf("ladder contracts expected 5, got %v", total)
	}
	if total > pos {
		t.Fatalf("ladder %v must not exceed position %v", total, pos)
	}
	// A 1-contract position with the same ladder WOULD oversell: 4 tiers × 1.
	small, _ := tr.ProtectionContractsForQuantity("ZECUSDT", 0.01)
	var smallTotal float64
	for _, pct := range []float64{20, 18, 15, 12} {
		c, _ := tr.ProtectionContractsForQuantity("ZECUSDT", 0.01*pct/100.0)
		smallTotal += c
	}
	if smallTotal <= small {
		t.Fatalf("1-contract position must trip the oversell guard: ladder=%v pos=%v", smallTotal, small)
	}
}
