package market

import (
	"math"
	"nofx/logger"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HotCoin represents a hot coin with composite scoring
type HotCoin struct {
	Symbol                string           `json:"symbol"`
	CurrentPrice          float64          `json:"current_price"`
	QuoteVolume24h        float64          `json:"quote_volume_24h"`
	PriceChangePct        float64          `json:"price_change_pct"` // signed 24h change % (for display)
	OpenInterestUSD       float64          `json:"open_interest_usd"`
	OpenInterestChangePct float64          `json:"open_interest_change_pct,omitempty"`
	OpenInterestWindowSec int              `json:"open_interest_window_sec,omitempty"`
	OpenInterestSource    string           `json:"open_interest_source,omitempty"`
	FundingRate           float64          `json:"funding_rate"`
	HotScore              float64          `json:"hot_score"`
	Source                string           `json:"source"`
	Quality               CandidateQuality `json:"quality,omitempty"`
}

const (
	hotCoinMinVolume   = 50_000_000 // 50M USDT (tier-1 threshold)
	hotCoinMinOI       = 15_000_000 // 15M USDT (tier-1 threshold)
	hotCoinMaxPriceChg = 30.0       // 30% max abs price change

	// Second-tier thresholds: lower absolute bars but require composite percentile > 0.7.
	hotCoinTier2MinVolume    = 20_000_000 // 20M USDT
	hotCoinTier2MinOI        = 8_000_000  // 8M USDT
	hotCoinTier2MinComposite = 0.70       // minimum composite score to qualify
)

// GetHotCoins returns top hot coins by composite score (auto-selects exchange)
func GetHotCoins(limit int, excludedCoins []string) ([]HotCoin, error) {
	return GetHotCoinsWithExchange(limit, excludedCoins, "okx")
}

// GetHotCoinsWithExchange returns hot coins from specified exchange
func GetHotCoinsWithExchange(limit int, excludedCoins []string, exchange string) ([]HotCoin, error) {
	return cachedHotCoinList(hotCoinCacheKey("hot", limit, excludedCoins, exchange), 180*time.Second, func() ([]HotCoin, error) {
		switch strings.ToLower(exchange) {
		case "okx":
			return getHotCoinsOKX(limit, excludedCoins)
		case "binance":
			return getHotCoinsBinance(limit, excludedCoins)
		default:
			return getHotCoinsOKX(limit, excludedCoins)
		}
	})
}

// GetOITopCoins returns coins ranked by OI increase (defaults to OKX)
func GetOITopCoins(limit int, excludedCoins []string) ([]HotCoin, error) {
	return GetOITopCoinsWithExchange(limit, excludedCoins, "okx")
}

// GetOITopCoinsWithExchange returns coins ranked by OI increase from specified exchange
func GetOITopCoinsWithExchange(limit int, excludedCoins []string, exchange string) ([]HotCoin, error) {
	return cachedHotCoinList(hotCoinCacheKey("oi_top", limit, excludedCoins, exchange), 180*time.Second, func() ([]HotCoin, error) {
		switch strings.ToLower(exchange) {
		case "binance":
			return getOIRankedCoinsBinance(limit, excludedCoins, true)
		default:
			return getOIRankedCoinsOKX(limit, excludedCoins, true)
		}
	})
}

// GetOILowCoins returns coins ranked by OI decrease (defaults to OKX)
func GetOILowCoins(limit int, excludedCoins []string) ([]HotCoin, error) {
	return GetOILowCoinsWithExchange(limit, excludedCoins, "okx")
}

// GetOILowCoinsWithExchange returns coins ranked by OI decrease from specified exchange
func GetOILowCoinsWithExchange(limit int, excludedCoins []string, exchange string) ([]HotCoin, error) {
	return cachedHotCoinList(hotCoinCacheKey("oi_low", limit, excludedCoins, exchange), 180*time.Second, func() ([]HotCoin, error) {
		switch strings.ToLower(exchange) {
		case "binance":
			return getOIRankedCoinsBinance(limit, excludedCoins, false)
		default:
			return getOIRankedCoinsOKX(limit, excludedCoins, false)
		}
	})
}

// ---- OKX implementation ----

func getHotCoinsOKX(limit int, excludedCoins []string) ([]HotCoin, error) {
	okx := NewOKXAPIClient()
	excluded := toExcludeMap(excludedCoins)

	tickers, err := okx.GetAllSwapTickers()
	if err != nil {
		return nil, err
	}

	type raw struct {
		symbol    string
		price     float64
		vol       float64
		signedChg float64 // signed 24h change % (for display)
		chg       float64 // abs(change) (for scoring)
		oi        float64
		tier2     bool // true when only second-tier threshold is met
	}

	// Pre-filter candidates (cheap, ticker-only) before the expensive per-symbol
	// OI fetches, which we run concurrently below.
	type preCand struct {
		symbol    string
		price     float64
		vol       float64
		signedChg float64
	}
	var pre []preCand
	for _, t := range tickers {
		if !strings.HasSuffix(t.InstID, "-USDT-SWAP") {
			continue
		}
		// Convert OKX symbol to standard: BTC-USDT-SWAP → BTCUSDT
		stdSymbol := okxToBinanceSymbol(t.InstID)
		if excluded[stdSymbol] {
			continue
		}

		last, _ := strconv.ParseFloat(t.Last, 64)
		open24h, _ := strconv.ParseFloat(t.Open24h, 64)
		volCcy, _ := strconv.ParseFloat(t.VolCcy24h, 64)

		// volCcy24h is in base currency; convert to USDT
		volUSD := volCcy * last

		var chg float64
		if open24h > 0 {
			chg = (last - open24h) / open24h * 100
		}
		if math.Abs(chg) > hotCoinMaxPriceChg {
			continue
		}
		// Reject coins below tier-2 (lowest) volume floor early.
		if volUSD < hotCoinTier2MinVolume {
			continue
		}
		pre = append(pre, preCand{symbol: stdSymbol, price: last, vol: volUSD, signedChg: chg})
	}

	// Concurrent OI fetch (bounded) — serial per-symbol calls previously made this
	// path slow; OKX has many candidates so parallelism matters.
	ois := fetchOIConcurrent(pre, func(p preCand) (float64, bool) {
		oiData, err := okx.GetOpenInterest(p.symbol)
		if err != nil || oiData == nil {
			return 0, false
		}
		oiUSD := oiData.Latest * p.price
		return oiUSD, oiUSD >= hotCoinTier2MinOI
	})

	var raws []raw
	for i, p := range pre {
		oiUSD, ok := ois[i]
		if !ok {
			continue
		}
		isTier2 := p.vol < hotCoinMinVolume || oiUSD < hotCoinMinOI
		raws = append(raws, raw{
			symbol:    p.symbol,
			price:     p.price,
			vol:       p.vol,
			signedChg: p.signedChg,
			chg:       math.Abs(p.signedChg),
			oi:        oiUSD,
			tier2:     isTier2,
		})
	}

	// Build candidateInput slice for batch percentile scoring.
	inputs := make([]candidateInput, len(raws))
	for i, r := range raws {
		activity := 0.0
		if r.oi > 0 {
			activity = r.vol / r.oi * 100
		}
		inputs[i] = candidateInput{
			symbol:      r.symbol,
			volumeUSD:   r.vol,
			oiUSD:       r.oi,
			absChgPct:   r.chg,
			activity:    activity,
			oiGrowthPct: math.NaN(), // not available at this stage
			fundingRate: math.NaN(), // optional; skip per-coin API calls to keep batch fast
		}
	}

	qualities := scoreCandidatesPercentile(inputs)

	var candidates []HotCoin
	for i, r := range raws {
		q := qualities[i]
		if !q.Passed {
			continue
		}
		composite := compositeHotScore(q)

		// Tier-2 coins must clear the composite threshold.
		if r.tier2 && composite < hotCoinTier2MinComposite {
			continue
		}

		logger.Infof("%s", qualityLogLine(r.symbol, q, composite))

		candidates = append(candidates, HotCoin{
			Symbol:          r.symbol,
			CurrentPrice:    r.price,
			QuoteVolume24h:  r.vol,
			PriceChangePct:  r.signedChg,
			OpenInterestUSD: r.oi,
			HotScore:        composite,
			Source:          "okx_hot",
			Quality:         q,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].HotScore > candidates[j].HotScore
	})

	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}

	logger.Infof("GetHotCoinsOKX: found %d coins (%d raw candidates)", len(candidates), len(raws))
	return candidates, nil
}

func getOIRankedCoinsOKX(limit int, excludedCoins []string, ascending bool) ([]HotCoin, error) {
	okx := NewOKXAPIClient()
	excluded := toExcludeMap(excludedCoins)

	tickers, err := okx.GetAllSwapTickers()
	if err != nil {
		return nil, err
	}

	var rawCoins []HotCoin

	for _, t := range tickers {
		if !strings.HasSuffix(t.InstID, "-USDT-SWAP") {
			continue
		}
		stdSymbol := okxToBinanceSymbol(t.InstID)
		if excluded[stdSymbol] {
			continue
		}

		last, _ := strconv.ParseFloat(t.Last, 64)
		volCcy, _ := strconv.ParseFloat(t.VolCcy24h, 64)
		volUSD := volCcy * last
		if volUSD < hotCoinMinVolume*0.5 { // lower threshold for OI ranking
			continue
		}

		open24h, _ := strconv.ParseFloat(t.Open24h, 64)
		var chg float64
		if open24h > 0 {
			chg = (last - open24h) / open24h * 100
		}

		// Get current OI
		oiData, err := okx.GetOpenInterest(stdSymbol)
		if err != nil || oiData == nil || oiData.Latest == 0 {
			continue
		}
		oiUSD := oiData.Latest * last

		// For OI change, use volume/OI ratio as activity proxy
		oiChange := 0.0
		if oiUSD > 0 {
			oiChange = volUSD / oiUSD * 100
		}

		rawCoins = append(rawCoins, HotCoin{
			Symbol:          stdSymbol,
			CurrentPrice:    last,
			QuoteVolume24h:  volUSD,
			PriceChangePct:  chg,
			OpenInterestUSD: oiUSD,
			HotScore:        oiChange,
			Source:          "okx_oi_rank",
		})
	}

	coins := RerankOICoins(rawCoins, ascending)
	if deltaCoins, ok := computeOIDeltaScores("okx", coins, ascending); ok {
		coins = deltaCoins
	} else if ascending {
		sort.Slice(coins, func(i, j int) bool {
			return coins[i].HotScore > coins[j].HotScore
		})
	} else {
		sort.Slice(coins, func(i, j int) bool {
			return coins[i].HotScore < coins[j].HotScore
		})
	}

	if limit > 0 && len(coins) > limit {
		coins = coins[:limit]
	}

	logger.Infof("GetOIRankedCoinsOKX: found %d coins (ascending=%v)", len(coins), ascending)
	return coins, nil
}

// ---- Binance implementation (fallback, may be geo-restricted) ----

func getOIRankedCoinsBinance(limit int, excludedCoins []string, ascending bool) ([]HotCoin, error) {
	client := NewAPIClient()
	excluded := toExcludeMap(excludedCoins)

	tickers, err := client.GetAllTickers24h()
	if err != nil {
		return nil, err
	}

	var rawCoins []HotCoin

	for _, t := range tickers {
		if !strings.HasSuffix(t.Symbol, "USDT") {
			continue
		}
		if excluded[t.Symbol] {
			continue
		}

		vol, _ := strconv.ParseFloat(t.QuoteVolume, 64)
		if vol < hotCoinMinVolume*0.5 {
			continue
		}

		chg, _ := strconv.ParseFloat(t.PriceChangePercent, 64)
		price, _ := strconv.ParseFloat(t.LastPrice, 64)
		if price <= 0 {
			price, _ = strconv.ParseFloat(t.WeightedAvgPrice, 64)
		}

		oiData, err := getOpenInterestData(t.Symbol)
		if err != nil || oiData == nil || oiData.Latest == 0 {
			continue
		}
		oiUSD := oiData.Latest * price

		oiChange := 0.0
		if oiUSD > 0 {
			oiChange = vol / oiUSD * 100
		}

		rawCoins = append(rawCoins, HotCoin{
			Symbol:          t.Symbol,
			CurrentPrice:    price,
			QuoteVolume24h:  vol,
			PriceChangePct:  chg,
			OpenInterestUSD: oiUSD,
			HotScore:        oiChange,
			Source:          "binance_oi_rank",
		})
	}

	coins := RerankOICoins(rawCoins, ascending)
	if deltaCoins, ok := computeOIDeltaScores("binance", coins, ascending); ok {
		coins = deltaCoins
	} else if ascending {
		sort.Slice(coins, func(i, j int) bool {
			return coins[i].HotScore > coins[j].HotScore
		})
	} else {
		sort.Slice(coins, func(i, j int) bool {
			return coins[i].HotScore < coins[j].HotScore
		})
	}

	if limit > 0 && len(coins) > limit {
		coins = coins[:limit]
	}

	logger.Infof("getOIRankedCoinsBinance: found %d coins (ascending=%v)", len(coins), ascending)
	return coins, nil
}

func getHotCoinsBinance(limit int, excludedCoins []string) ([]HotCoin, error) {
	client := NewAPIClient()
	excluded := toExcludeMap(excludedCoins)

	tickers, err := client.GetAllTickers24h()
	if err != nil {
		return nil, err
	}

	type raw struct {
		symbol    string
		price     float64
		vol       float64
		signedChg float64 // signed 24h change % (for display)
		chg       float64 // abs(change) (for scoring)
		oi        float64
		tier2     bool
	}

	// Pre-filter (ticker-only) before the expensive per-symbol OI fetches.
	type preCand struct {
		symbol    string
		price     float64
		vol       float64
		signedChg float64
	}
	var pre []preCand
	for _, t := range tickers {
		if !strings.HasSuffix(t.Symbol, "USDT") {
			continue
		}
		if excluded[t.Symbol] {
			continue
		}
		vol, _ := strconv.ParseFloat(t.QuoteVolume, 64)
		chg, _ := strconv.ParseFloat(t.PriceChangePercent, 64)

		if math.Abs(chg) > hotCoinMaxPriceChg {
			continue
		}
		if vol < hotCoinTier2MinVolume {
			continue
		}
		// Prefer last price; fall back to weighted-avg when absent.
		price, _ := strconv.ParseFloat(t.LastPrice, 64)
		if price <= 0 {
			price, _ = strconv.ParseFloat(t.WeightedAvgPrice, 64)
		}
		pre = append(pre, preCand{symbol: t.Symbol, price: price, vol: vol, signedChg: chg})
	}

	// Concurrent OI fetch (bounded). Serial per-symbol OI calls through the Binance
	// proxy previously blew past the HTTP timeout (~0.5s × 100+ candidates); the
	// worker pool caps latency at roughly ceil(n/workers) × per-call time.
	ois := fetchOIConcurrent(pre, func(p preCand) (float64, bool) {
		oiData, err := getOpenInterestData(p.symbol)
		if err != nil || oiData == nil {
			return 0, false
		}
		oiUSD := oiData.Latest * p.price
		return oiUSD, oiUSD >= hotCoinTier2MinOI
	})

	var raws []raw
	for i, p := range pre {
		oiUSD, ok := ois[i]
		if !ok {
			continue
		}
		isTier2 := p.vol < hotCoinMinVolume || oiUSD < hotCoinMinOI
		raws = append(raws, raw{
			symbol:    p.symbol,
			price:     p.price,
			vol:       p.vol,
			signedChg: p.signedChg,
			chg:       math.Abs(p.signedChg),
			oi:        oiUSD,
			tier2:     isTier2,
		})
	}

	// Batch percentile scoring.
	inputs := make([]candidateInput, len(raws))
	for i, r := range raws {
		activity := 0.0
		if r.oi > 0 {
			activity = r.vol / r.oi * 100
		}
		inputs[i] = candidateInput{
			symbol:      r.symbol,
			volumeUSD:   r.vol,
			oiUSD:       r.oi,
			absChgPct:   r.chg,
			activity:    activity,
			oiGrowthPct: math.NaN(),
			fundingRate: math.NaN(),
		}
	}

	qualities := scoreCandidatesPercentile(inputs)

	var candidates []HotCoin
	for i, r := range raws {
		q := qualities[i]
		if !q.Passed {
			continue
		}
		composite := compositeHotScore(q)
		if r.tier2 && composite < hotCoinTier2MinComposite {
			continue
		}

		logger.Infof("%s", qualityLogLine(r.symbol, q, composite))

		candidates = append(candidates, HotCoin{
			Symbol:          r.symbol,
			CurrentPrice:    r.price,
			QuoteVolume24h:  r.vol,
			PriceChangePct:  r.signedChg,
			OpenInterestUSD: r.oi,
			HotScore:        composite,
			Source:          "binance_hot",
			Quality:         q,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].HotScore > candidates[j].HotScore
	})

	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

// ---- Helpers ----

// oiFetchConcurrency bounds simultaneous per-symbol OI requests. Kept modest so
// the proxied Binance endpoint isn't hammered while still cutting wall-clock time
// from O(n) serial round-trips to ~O(n/workers).
const oiFetchConcurrency = 12

// fetchOIConcurrent runs fetch(item) for every item using a bounded worker pool
// and returns a map keyed by the item's index. An entry is present only when
// fetch reported ok==true (i.e. the coin cleared its OI floor). Results preserve
// index alignment with the input slice so callers can zip them back together.
func fetchOIConcurrent[T any](items []T, fetch func(T) (float64, bool)) map[int]float64 {
	out := make(map[int]float64, len(items))
	if len(items) == 0 {
		return out
	}

	type result struct {
		idx float64
		val float64
		ok  bool
		i   int
	}
	sem := make(chan struct{}, oiFetchConcurrency)
	resCh := make(chan result, len(items))

	for i, item := range items {
		sem <- struct{}{}
		go func(i int, item T) {
			defer func() { <-sem }()
			val, ok := fetch(item)
			resCh <- result{val: val, ok: ok, i: i}
		}(i, item)
	}

	for range items {
		r := <-resCh
		if r.ok {
			out[r.i] = r.val
		}
	}
	return out
}

func safeNorm(val, max float64) float64 {
	if max == 0 {
		return 0
	}
	return val / max
}

func toExcludeMap(coins []string) map[string]bool {
	m := make(map[string]bool, len(coins))
	for _, c := range coins {
		m[strings.ToUpper(c)] = true
	}
	return m
}
