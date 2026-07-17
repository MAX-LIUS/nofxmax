package trader

import (
	"math"
	"testing"
)

// TestGetPositionDetailsForFingerprint_ShortAbsQty guards the endless-re-arm bug
// (2026-07-17): Binance encodes SHORT positions with a NEGATIVE positionAmt. The
// prior `q > 0` guard skipped shorts, so getPositionDetailsForFingerprint returned
// qty=0. The persisted dynamic-protection record then carried a
// PositionFingerprint of "entry|0.00000000", which the next reconcile cycle read
// as a CLOSED position (qty=0), ignored, found 0 armed records, declared the
// trailing state "belongs to an old position", cleared it, and re-armed — placing
// a fresh trailing order on EVERY short every cycle (~11/min, tripping -4045).
func TestGetPositionDetailsForFingerprint_ShortAbsQty(t *testing.T) {
	tr := &fakeReconcileTrader{
		positions: []map[string]interface{}{
			{"symbol": "XAGUSDT", "side": "short", "positionAmt": -0.038, "entryPrice": 1858.0, "markPrice": 1850.0},
			{"symbol": "ETHUSDT", "side": "long", "positionAmt": 0.5, "entryPrice": 3000.0, "markPrice": 3010.0},
		},
	}
	at := &AutoTrader{trader: tr}

	t.Run("short returns positive abs quantity", func(t *testing.T) {
		qty, _ := at.getPositionDetailsForFingerprint("XAGUSDT", "short")
		if math.Abs(qty-0.038) > 1e-9 {
			t.Fatalf("short qty: got %.6f, want 0.038 (abs of -0.038); qty=0 causes endless re-arm", qty)
		}
	})

	t.Run("long still returns quantity", func(t *testing.T) {
		qty, _ := at.getPositionDetailsForFingerprint("ETHUSDT", "long")
		if math.Abs(qty-0.5) > 1e-9 {
			t.Fatalf("long qty: got %.6f, want 0.5", qty)
		}
	})

	t.Run("missing position returns 0", func(t *testing.T) {
		qty, _ := at.getPositionDetailsForFingerprint("BTCUSDT", "short")
		if qty != 0 {
			t.Fatalf("missing position: got %.6f, want 0", qty)
		}
	})
}
