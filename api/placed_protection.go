package api

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"nofx/store"
)

// placedProtectionItem mirrors the frontend PlanItem shape so the position-history
// panel can render the REAL protections the bot placed on the exchange (recorded
// in close_intents at open time), instead of the AI decision plan — which is
// skipped entirely when a protection leg is in manual mode and therefore never
// reflects what actually protected the position.
type placedProtectionItem struct {
	Mechanism  string  `json:"mechanism"`
	Kind       string  `json:"kind"` // tp | sl | be | drawdown | trailing | structural
	Label      string  `json:"label"`
	TriggerPct float64 `json:"triggerPct"`
	// TriggerPrice 是"这一档开始起作用"的价位。静态档(TP/SL/BE/结构位)它就是成交价;
	// 回撤档它只是**激活价** —— 到这里只是开始跟踪峰值,不成交。
	TriggerPrice *float64 `json:"triggerPrice,omitempty"`
	// ExecutionPrice 是真正成交的价位(回撤档 = 峰值 ×(1∓callback))。排序键用它。
	ExecutionPrice *float64 `json:"executionPrice,omitempty"`
	ExecutionPct   *float64 `json:"executionPct,omitempty"`
	CloseRatioPct  *float64 `json:"closeRatioPct,omitempty"`
	Note           string   `json:"note,omitempty"`
}

// placedFromSnapshotTiers converts a canonical protection_plan_snapshot's tiers into
// the panel item shape. The snapshot is already duplicate-free and correctly labeled
// (built at open from the resolved plan), so this is a straight field copy with no
// entry-window filtering or dedup. Tiers with an unrenderable kind are skipped.
//
// isLong 用于**按成交价重排**。写库时已经排好序(trader/protection_plan_snapshot.go),
// 但历史行是按机制分组落的、且回撤档当时没有价格,所以这里对读出来的行再排一次 ——
// 否则老仓位的面板顺序仍是旧的机制序。排序键是成交价而不是激活价:回撤档的激活价
// 会把它顶到远端,而它实际成交在离入场更近的位置。
func placedFromSnapshotTiers(tiers []store.ProtectionPlanTier, isLong bool) []placedProtectionItem {
	if len(tiers) == 0 {
		return nil
	}
	out := make([]placedProtectionItem, 0, len(tiers))
	for _, t := range tiers {
		if strings.TrimSpace(t.Kind) == "" {
			continue
		}
		out = append(out, placedProtectionItem{
			Mechanism:      t.Mechanism,
			Kind:           t.Kind,
			Label:          t.Label,
			TriggerPct:     t.TriggerPct,
			TriggerPrice:   t.TriggerPrice,
			ExecutionPrice: t.ExecutionPrice,
			ExecutionPct:   t.ExecutionPct,
			CloseRatioPct:  t.CloseRatioPct,
			Note:           t.Note,
		})
	}
	sortPlacedProtection(out, isLong)
	return out
}

// placedSortPrice 取一行用于排序的价格:优先成交价,退回触发/激活价。
func placedSortPrice(it placedProtectionItem) float64 {
	if it.ExecutionPrice != nil && *it.ExecutionPrice > 0 {
		return *it.ExecutionPrice
	}
	if it.TriggerPrice != nil && *it.TriggerPrice > 0 {
		return *it.TriggerPrice
	}
	return 0
}

// sortPlacedProtection 按成交价排:多头由高到低,空头由低到高(都是"由远到近"),
// 无价格的行沉到末尾并保持原相对顺序。
func sortPlacedProtection(items []placedProtectionItem, isLong bool) {
	sort.SliceStable(items, func(i, j int) bool {
		pi, pj := placedSortPrice(items[i]), placedSortPrice(items[j])
		if (pi > 0) != (pj > 0) {
			return pi > 0
		}
		if pi == pj {
			return false
		}
		if isLong {
			return pi > pj
		}
		return pi < pj
	})
}

// kindForMechanism maps a canonical close mechanism to the panel's plan "kind"
// (drives color + grouping). Unknown/among-close mechanisms return "" so they are
// skipped rather than mis-colored.
func kindForMechanism(mech string) string {
	switch mech {
	case store.MechLadderTP, store.MechFullTP:
		return "tp"
	case store.MechLadderSL, store.MechFullSL, store.MechStructuralSL, store.MechFallbackSL:
		return "sl"
	case store.MechBreakEven:
		return "be"
	case store.MechManagedDrawdown:
		return "drawdown"
	case store.MechNativeTrailing, store.MechTrailingTP:
		return "trailing"
	default:
		return ""
	}
}

// labelForMechanism gives a short tier-agnostic label; the caller appends a tier
// index for laddered mechanisms.
func labelForMechanism(mech string) string {
	switch mech {
	case store.MechLadderTP:
		return "TP"
	case store.MechLadderSL:
		return "SL"
	case store.MechFullTP:
		return "Full TP"
	case store.MechFullSL:
		return "Full SL"
	case store.MechStructuralSL:
		return "Structural SL"
	case store.MechFallbackSL:
		return "Fallback SL"
	case store.MechBreakEven:
		return "Break-even"
	case store.MechManagedDrawdown:
		return "Drawdown"
	case store.MechNativeTrailing:
		return "Trailing"
	case store.MechTrailingTP:
		return "Trailing TP"
	default:
		return mech
	}
}

// entryProtectionWindowMs bounds "entry protection" to the atomic placement burst
// right after a fill. Protection is re-armed/re-anchored throughout the hold
// (trailing TP/BE climb as price moves), and every re-placement writes a fresh
// close_intents row. Listing all of them produces dozens of duplicate/superseded
// tiers. The panel is the ENTRY plan, so we keep only intents recorded within this
// window of entry — the initial resting protection — and dedupe identical tiers.
const entryProtectionWindowMs int64 = 5 * 60 * 1000 // 5 minutes

// buildPlacedProtectionPlan turns the position's entry-window close_intents into
// price-anchored plan rows. entryPrice+isLong let us compute the signed % move so
// the panel shows both the absolute trigger and its distance from entry. Identical
// tiers (same mechanism+price, re-recorded by reconciliation) are collapsed, rows
// are ordered by distance from entry (nearest first), then each laddered mechanism
// gets a 1-based tier index (TP1..TPn / SL1..SLn). entryTimeMs<=0 disables the
// window filter (keeps legacy behavior for callers without an entry time).
func buildPlacedProtectionPlan(intents []store.CloseIntent, entryPrice float64, isLong bool, entryTimeMs int64) []placedProtectionItem {
	if len(intents) == 0 {
		return nil
	}
	type row struct {
		mech  string
		kind  string
		price float64
		hasP  bool
	}
	rows := make([]row, 0, len(intents))
	seenTier := map[string]bool{} // mechanism@rounded-price dedupe
	for _, ci := range intents {
		// Keep only the entry-time placement burst; later rows are trailing
		// re-arms, not the entry plan.
		if entryTimeMs > 0 && ci.IntentTime > entryTimeMs+entryProtectionWindowMs {
			continue
		}
		mech := store.ClassifyClose(ci.Reason).Mechanism
		kind := kindForMechanism(mech)
		if kind == "" {
			continue // bot MARKET close / ai_close / unknown — not a placed protection level
		}
		r := row{mech: mech, kind: kind}
		if ci.TriggerPrice > 0 {
			r.price, r.hasP = ci.TriggerPrice, true
		}
		tierKey := mech + "@" + strconv.FormatFloat(round2(r.price), 'f', 2, 64)
		if seenTier[tierKey] {
			continue // collapse identical re-recorded tier
		}
		seenTier[tierKey] = true
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		return nil
	}

	// Order within each mechanism by distance from entry so ladder tiers read
	// nearest-first (TP1 closest to entry, etc.).
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].mech != rows[j].mech {
			return rows[i].mech < rows[j].mech
		}
		return distFromEntry(rows[i].price, entryPrice) < distFromEntry(rows[j].price, entryPrice)
	})

	// Count per-mechanism to decide whether to append a tier index.
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.mech]++
	}
	seen := map[string]int{}
	out := make([]placedProtectionItem, 0, len(rows))
	for _, r := range rows {
		seen[r.mech]++
		label := labelForMechanism(r.mech)
		if (r.mech == store.MechLadderTP || r.mech == store.MechLadderSL) && counts[r.mech] > 1 {
			label = labelForMechanism(r.mech) + itoa(seen[r.mech])
		}
		item := placedProtectionItem{Mechanism: r.mech, Kind: r.kind, Label: label}
		if r.hasP {
			p := r.price
			item.TriggerPrice = &p
			if entryPrice > 0 {
				pct := (p - entryPrice) / entryPrice * 100
				if !isLong {
					pct = -pct // express as favorable(+)/adverse(-) for the position side
				}
				item.TriggerPct = round2(pct)
			}
		}
		out = append(out, item)
	}
	// 上面的机制分组序只是为了给阶梯档编号(TP1 最靠近入场)。编号完成后必须按价格
	// 重排,和快照路径一致 —— 否则同一个面板里,新仓位(走快照)按价格排、老仓位
	// (走 close_intents 回放)按机制排,两种顺序看起来像 bug。
	// 注:close_intents 里回撤档记的是激活价,没有成交价可用,只能按触发价排。
	sortPlacedProtection(out, isLong)
	return out
}

func distFromEntry(price, entry float64) float64 {
	d := price - entry
	if d < 0 {
		d = -d
	}
	return d
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// protectionDeviation compares the resolved MANUAL first TP/SL (what was actually
// placed) against the AI's structural TP/SL opinion (from the decision plan), so
// the panel can show whether the manual template drifted from where the AI read
// the structure. Returns nil when either side is missing.
type protectionDeviation struct {
	ManualSL   *float64 `json:"manual_sl,omitempty"`
	ManualTP   *float64 `json:"manual_tp,omitempty"`
	AISL       *float64 `json:"ai_sl,omitempty"`
	AITP       *float64 `json:"ai_tp,omitempty"`
	SLDiffPct  *float64 `json:"sl_diff_pct,omitempty"` // (manual-ai)/ai *100
	TPDiffPct  *float64 `json:"tp_diff_pct,omitempty"`
	ManualRR   *float64 `json:"manual_rr,omitempty"` // |TP-entry| / |SL-entry|
	AIRR       *float64 `json:"ai_rr,omitempty"`
	EntryPrice float64  `json:"entry_price,omitempty"`         // actual fill (manual/live entry)
	AIEntry    *float64 `json:"ai_entry,omitempty"`            // AI's PLANNED entry (nil when unknown)
	EntryDiffPct *float64 `json:"entry_diff_pct,omitempty"`    // (actual-planned)/planned *100 — slippage/chase
}

// buildProtectionDeviation derives the manual first-TP/first-SL from the placed
// plan rows and the AI SL/TP from the decision values, then computes R multiples
// and percentage drift. entryPrice anchors the R computation. "First" means
// nearest-to-entry (the tier that fires first), not array order — so the manual
// R multiple compares the first profit-lock against the first stop, matching how
// the AI's own first_target R is defined.
func buildProtectionDeviation(placed []placedProtectionItem, aiSL, aiTP, aiEntry, entryPrice float64, isLong bool) *protectionDeviation {
	var mSL, mTP *float64
	for i := range placed {
		it := placed[i]
		if it.TriggerPrice == nil {
			continue
		}
		v := *it.TriggerPrice
		switch it.Kind {
		case "tp":
			// nearest-to-entry TP
			if mTP == nil || distFromEntry(v, entryPrice) < distFromEntry(*mTP, entryPrice) {
				vv := v
				mTP = &vv
			}
		case "sl":
			// nearest-to-entry SL (the tighter stop fires first)
			if mSL == nil || distFromEntry(v, entryPrice) < distFromEntry(*mSL, entryPrice) {
				vv := v
				mSL = &vv
			}
		}
	}
	hasAI := aiSL > 0 || aiTP > 0 || aiEntry > 0
	if mSL == nil && mTP == nil && !hasAI {
		return nil
	}
	d := &protectionDeviation{EntryPrice: entryPrice, ManualSL: mSL, ManualTP: mTP}
	if aiEntry > 0 {
		v := aiEntry
		d.AIEntry = &v
		if entryPrice > 0 {
			diff := round2((entryPrice - aiEntry) / aiEntry * 100)
			d.EntryDiffPct = &diff
		}
	}
	if aiSL > 0 {
		v := aiSL
		d.AISL = &v
	}
	if aiTP > 0 {
		v := aiTP
		d.AITP = &v
	}
	if mSL != nil && aiSL > 0 {
		diff := round2((*mSL - aiSL) / aiSL * 100)
		d.SLDiffPct = &diff
	}
	if mTP != nil && aiTP > 0 {
		diff := round2((*mTP - aiTP) / aiTP * 100)
		d.TPDiffPct = &diff
	}
	if entryPrice > 0 {
		if mSL != nil && mTP != nil {
			if rr := rrMultiple(entryPrice, *mTP, *mSL); rr > 0 {
				d.ManualRR = &rr
			}
		}
		if aiSL > 0 && aiTP > 0 {
			if rr := rrMultiple(entryPrice, aiTP, aiSL); rr > 0 {
				d.AIRR = &rr
			}
		}
	}
	_ = isLong
	return d
}

func rrMultiple(entry, tp, sl float64) float64 {
	risk := entry - sl
	if risk < 0 {
		risk = -risk
	}
	reward := tp - entry
	if reward < 0 {
		reward = -reward
	}
	if risk <= 0 {
		return 0
	}
	return round2(reward / risk)
}

// aiSLTPFromDecisionJSON extracts the AI's top-level structural stop_loss /
// take_profit opinion for a (symbol, action) from the raw decision payloads.
// These exist even in manual protection mode (the AI still reasons R multiples),
// so the panel can always show the structural reference. Returns (0,0) when not
// found.
func aiSLTPFromDecisionJSON(payloads []string, symbol, action string) (sl, tp, entry float64) {
	for _, payload := range payloads {
		if strings.TrimSpace(payload) == "" {
			continue
		}
		sl, tp, entry, ok := decodeAISLTP(payload, symbol, action)
		if ok {
			return sl, tp, entry
		}
	}
	return 0, 0, 0
}

// decodeAISLTP unmarshals decision payloads and returns the AI's structural
// stop_loss / take_profit and PLANNED entry for the matching (symbol, action).
// ok=false when no matching decision carries usable SL/TP values.
func decodeAISLTP(payload, symbol, action string) (sl, tp, entry float64, ok bool) {
	var decisions []kernelDecisionSLTP
	if err := json.Unmarshal([]byte(payload), &decisions); err != nil {
		return 0, 0, 0, false
	}
	for _, d := range decisions {
		if symbol != "" && !strings.EqualFold(d.Symbol, symbol) {
			continue
		}
		if action != "" && !strings.EqualFold(d.Action, action) {
			continue
		}
		if d.StopLoss > 0 || d.TakeProfit > 0 {
			return d.StopLoss, d.TakeProfit, d.plannedEntry(), true
		}
	}
	return 0, 0, 0, false
}

// kernelDecisionSLTP is a minimal projection of kernel.Decision for extracting
// only the structural SL/TP without pulling the full protection plan machinery.
type kernelDecisionSLTP struct {
	Symbol     string  `json:"symbol"`
	Action     string  `json:"action"`
	StopLoss   float64 `json:"stop_loss"`
	TakeProfit float64 `json:"take_profit"`
	// Price is the AI's PLANNED entry (the price it intended to open at). Compared
	// against the position's actual fill (entry_price) in the deviation panel to
	// expose slippage / chase. review_context.risk_reward.entry is the same value
	// re-stated as the R basis; prefer it when present, else fall back to Price.
	Price         float64 `json:"price"`
	ReviewContext *struct {
		RiskReward *struct {
			Entry float64 `json:"entry"`
		} `json:"risk_reward"`
	} `json:"review_context"`
}

// plannedEntry returns the AI's intended entry price for this decision, preferring
// the risk_reward.entry (the AI's own R basis) over the raw order price.
func (d kernelDecisionSLTP) plannedEntry() float64 {
	if d.ReviewContext != nil && d.ReviewContext.RiskReward != nil && d.ReviewContext.RiskReward.Entry > 0 {
		return d.ReviewContext.RiskReward.Entry
	}
	return d.Price
}
