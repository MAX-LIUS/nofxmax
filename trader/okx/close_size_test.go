package okx

import "testing"

// TestResolveCloseSize_SubLotPartialBumped reproduces the sz=0 bug:
// a requested partial close of 0.4 contracts on an instrument with lotSz>=1
// previously formatted to "0" and was rejected by OKX (sCode 51000). It must
// now be bumped up to one whole lot.
func TestResolveCloseSize_SubLotPartialBumped(t *testing.T) {
	tr := &OKXTrader{}
	inst := &OKXInstrument{LotSz: 1, MinSz: 1}

	dec := tr.resolveCloseSize(0.4, 5, inst)
	if dec.Skip {
		t.Fatalf("expected a sendable order, got skip: %s", dec.Reason)
	}
	if !dec.Bumped {
		t.Fatalf("expected bumped=true for sub-lot partial, reason=%s", dec.Reason)
	}
	if dec.SzStr != "1" {
		t.Fatalf("expected szStr=1 after bump, got %q", dec.SzStr)
	}
}

// TestResolveCloseSize_FullPositionDust: the entire remaining position is below
// one lot. It cannot be closed by a lot-aligned order, so the caller must be
// told to stop retrying (Dust=true, Skip=true) instead of spamming sz=0.
func TestResolveCloseSize_FullPositionDust(t *testing.T) {
	tr := &OKXTrader{}
	inst := &OKXInstrument{LotSz: 1, MinSz: 1}

	dec := tr.resolveCloseSize(0.4, 0.4, inst)
	if !dec.Skip || !dec.Dust {
		t.Fatalf("expected Skip+Dust for sub-lot full position, got %+v", dec)
	}
	if dec.SzStr != "" {
		t.Fatalf("expected empty szStr for dust, got %q", dec.SzStr)
	}
}

// TestResolveCloseSize_BumpCappedAtFull: requested partial rounds below a lot,
// but the full remaining is also below a lot after... here full=0.6 (<lot) so
// it is dust. Use full=1.2 to test the bump being capped at the full remaining
// when full sits between want and lot is not the case; instead verify a normal
// bump where full >= lot.
func TestResolveCloseSize_BumpCappedAtFull(t *testing.T) {
	tr := &OKXTrader{}
	inst := &OKXInstrument{LotSz: 1, MinSz: 1}

	// want 0.3, full 1 -> bump to 1 lot, capped at full(1) => "1"
	dec := tr.resolveCloseSize(0.3, 1, inst)
	if dec.Skip {
		t.Fatalf("expected sendable, got skip: %s", dec.Reason)
	}
	if dec.SzStr != "1" {
		t.Fatalf("expected szStr=1, got %q", dec.SzStr)
	}
}

// TestResolveCloseSize_NormalPartial: a whole-lot partial close passes through
// unchanged, no bump.
func TestResolveCloseSize_NormalPartial(t *testing.T) {
	tr := &OKXTrader{}
	inst := &OKXInstrument{LotSz: 1, MinSz: 1}

	dec := tr.resolveCloseSize(3, 10, inst)
	if dec.Skip || dec.Bumped {
		t.Fatalf("expected clean partial, got %+v", dec)
	}
	if dec.SzStr != "3" {
		t.Fatalf("expected szStr=3, got %q", dec.SzStr)
	}
}

// TestResolveCloseSize_WantCappedAtFull: requesting more than remaining caps to
// the full remaining.
func TestResolveCloseSize_WantCappedAtFull(t *testing.T) {
	tr := &OKXTrader{}
	inst := &OKXInstrument{LotSz: 1, MinSz: 1}

	dec := tr.resolveCloseSize(20, 7, inst)
	if dec.Skip {
		t.Fatalf("expected sendable, got skip: %s", dec.Reason)
	}
	if dec.SzStr != "7" {
		t.Fatalf("expected szStr capped to 7, got %q", dec.SzStr)
	}
}

// TestResolveCloseSize_FractionalLot: instruments with fractional lotSz keep
// fractional precision (e.g. lotSz 0.01).
func TestResolveCloseSize_FractionalLot(t *testing.T) {
	tr := &OKXTrader{}
	inst := &OKXInstrument{LotSz: 0.01, MinSz: 0.01}

	dec := tr.resolveCloseSize(0.05, 0.2, inst)
	if dec.Skip || dec.Bumped {
		t.Fatalf("expected clean fractional partial, got %+v", dec)
	}
	if dec.SzStr != "0.05" {
		t.Fatalf("expected szStr=0.05, got %q", dec.SzStr)
	}
}

// TestResolveCloseSize_WantNonPositive: zero/negative want is skipped.
func TestResolveCloseSize_WantNonPositive(t *testing.T) {
	tr := &OKXTrader{}
	inst := &OKXInstrument{LotSz: 1, MinSz: 1}

	dec := tr.resolveCloseSize(0, 5, inst)
	if !dec.Skip || dec.Dust {
		t.Fatalf("expected plain skip for want<=0, got %+v", dec)
	}
}
