package trader

// protection_oversell.go holds the guard that decides whether a protection
// ladder is still safe once the venue's minimum-size clamping is accounted for.
//
// Background. ValidateProtectionQuantity deliberately accepts a tier whose raw
// quantity is below the instrument minimum, because every placement path clamps
// the contract size up to MinSz before it hits the wire. Without that leniency
// the tier was silently discarded: on ZECUSDT 2026-08-01 the 12%/3.6-ATR tier
// resolved to 0.993 contracts against a 1-contract minimum, so a ladder
// configured to protect 65% of the position only ever placed 48.3% of it.
//
// The leniency has a boundary. Clamping N tiers up to 1 contract each places N
// contracts. That is fine while the position is comfortably larger than N, which
// is guaranteed in the partial-drop case: if the LARGEST tier survived without
// clamping, the position is at least 1/maxRatio contracts, and every configured
// ladder has N < 1/maxRatio (4 tiers with a 20% max ⇒ 4 < 5). It breaks in the
// all-drop case: a 1-contract position with four tiers would place 4 contracts
// and close four times the position.
//
// So the guard runs per side, before per-tier filtering. If the clamped ladder
// would exceed the position it drops the whole side and lets the pre-existing
// collapse fallback own it with a single full-position order. Historical
// all-below-minimum behaviour is therefore unchanged, and the new leniency is
// confined to the partial case, which is the actual gap.
type protectionContractSizer interface {
	// ProtectionContractsForQuantity reports the contract size the venue would
	// actually put on the wire for this base-asset quantity, including MinSz
	// clamping and lot rounding.
	ProtectionContractsForQuantity(symbol string, quantity float64) (float64, error)
}

// ladderWouldOversell reports whether placing every tier in orders would put
// more contracts on the wire than the position holds.
//
// It fails OPEN (returns false) whenever it cannot answer confidently: no sizer
// capability, no tiers, an unresolvable position, or an unresolvable tier. That
// direction is deliberate. A false negative leaves the previous behaviour in
// place, whereas a false positive would drop a ladder that was fine.
func ladderWouldOversell(sizer protectionContractSizer, symbol string, quantity float64, orders []ProtectionOrder) bool {
	if sizer == nil || len(orders) == 0 || quantity <= 0 {
		return false
	}
	posContracts, err := sizer.ProtectionContractsForQuantity(symbol, quantity)
	if err != nil || posContracts <= 0 {
		return false
	}
	total := 0.0
	intendedPct := 0.0
	for _, order := range orders {
		if order.CloseRatioPct <= 0 {
			continue
		}
		intendedPct += order.CloseRatioPct
		orderQty := quantity * order.CloseRatioPct / 100.0
		if orderQty <= 0 {
			continue
		}
		contracts, err := sizer.ProtectionContractsForQuantity(symbol, orderQty)
		if err != nil {
			return false
		}
		total += contracts
	}
	if total > posContracts {
		return true
	}
	// Equality also disqualifies a PARTIAL ladder. A 4-contract position with the
	// 20/18/15/12 ladder clamps to 1+1+1+1 = 4, which the exchange accepts but
	// which closes 100% of a position the ladder only intended to close 65% of,
	// leaving no runner. Collapsing to one furthest-tier order is the better
	// shape there. A full ladder (a single 100% stop-loss tier, say) is expected
	// to equal the position and must not be dropped.
	return total >= posContracts && intendedPct < 100
}
