package trader

import "strings"

// MarketDataCapabilities describes which compact, execution-relevant market-data
// fields an exchange adapter can currently provide with reasonable confidence.
//
// This is intentionally narrow and Binance/OKX-first so future prompt/audit
// integrations can add only high-value fields without dragging in noisy raw data.
// A false value means "do not rely on this field by default".
//
// Notes:
//   - Price/quantity precision and tick/step flags refer to instrument/exchange-rule
//     metadata already available through the adapter.
//   - MinNotional and MinQty/MinSize are kept separate because exchanges expose
//     different constraint models.
//   - Last/Mark/BidAsk/Spread are about readily available quote data, not deep books.
//   - ContractValue is especially relevant for contract-based venues like OKX.
//   - FeeFallback means the runtime may use configured/safe-default fee assumptions
//     when venue-native fee schedules are not wired through this helper.
//   - DegradedProfile marks conservative exchanges where only a thin subset should be used.
type MarketDataCapabilities struct {
	InstrumentTickSize       bool
	InstrumentPricePrecision bool
	InstrumentQtyStep        bool
	InstrumentQtyPrecision   bool
	InstrumentMinQty         bool
	InstrumentMinNotional    bool
	InstrumentContractValue  bool

	QuoteLastPrice bool
	QuoteMarkPrice bool
	QuoteBestBid   bool
	QuoteBestAsk   bool
	QuoteSpread    bool

	FeeFallback     bool
	DegradedProfile bool
}

// GetMarketDataCapabilities returns a conservative capability profile for the
// current exchange focused on compact execution constraints and top-of-book data.
//
// The profile is intentionally biased toward safety:
// - Binance and OKX get the strongest first-wave support.
// - Other exchanges degrade cleanly to small, high-confidence subsets.
// - This helper does not imply that all fields should be prompted by default.
func (at *AutoTrader) GetMarketDataCapabilities() MarketDataCapabilities {
	switch strings.ToLower(at.exchange) {
	case "binance":
		return MarketDataCapabilities{
			InstrumentTickSize:       true,
			InstrumentPricePrecision: true,
			InstrumentQtyStep:        true,
			InstrumentQtyPrecision:   true,
			InstrumentMinQty:         false,
			InstrumentMinNotional:    false,
			InstrumentContractValue:  false,
			QuoteLastPrice:           true,
			QuoteMarkPrice:           true,
			QuoteBestBid:             false,
			QuoteBestAsk:             false,
			QuoteSpread:              false,
			FeeFallback:              true,
		}
	case "okx":
		return MarketDataCapabilities{
			InstrumentTickSize:       true,
			InstrumentPricePrecision: true,
			InstrumentQtyStep:        true,
			InstrumentQtyPrecision:   true,
			InstrumentMinQty:         true,
			InstrumentMinNotional:    false,
			InstrumentContractValue:  true,
			QuoteLastPrice:           true,
			QuoteMarkPrice:           false,
			QuoteBestBid:             true,
			QuoteBestAsk:             true,
			QuoteSpread:              true,
			FeeFallback:              true,
		}
	// NOTE on QuoteMarkPrice: no adapter exposes a GetMarkPrice method, and the only
	// reader of this flag (execution_constraints_snapshot.go:56) uses it solely as a
	// bail-out condition alongside QuoteLastPrice — so today the flag is inert
	// everywhere, including for Binance which declares it true while GetMarketPrice
	// returns LAST price. Every adapter's GetMarketPrice returns last price; mark price
	// reaches consumers through pos["markPrice"] from GetPositions instead
	// (getPositionMarkPrice, protection_reconciler.go:836). Recorded here so the flag is
	// not mistaken for a live capability claim.
	case "gate", "kucoin", "bybit", "bitget", "aster", "lighter", "hyperliquid":
		return MarketDataCapabilities{
			QuoteLastPrice:  true,
			QuoteMarkPrice:  true,
			FeeFallback:     true,
			DegradedProfile: true,
		}
	default:
		return MarketDataCapabilities{DegradedProfile: true}
	}
}
