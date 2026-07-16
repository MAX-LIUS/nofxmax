package api

import (
	"testing"
)

// Uses the exact shape observed in the live decision_records.decision_json:
// laddered SL with real prices + structural/atr anchors, plus drawdown stages.
const sampleDecisionJSON = `[
  {"symbol":"SOLUSDT","action":"open_long","protection_plan":{
    "mode":"combined",
    "ladder_rules":[
      {"stop_loss_pct":0.4342827947,"stop_loss_price":77.95,"stop_loss_close_ratio_pct":75,"structural_anchor":"15m支撑78.12外侧+0.5xATR缓冲","basis_type":"atr_based","volatility_buffer_pct":0.17},
      {"stop_loss_pct":0.6258781453,"stop_loss_price":77.8,"stop_loss_close_ratio_pct":25,"structural_anchor":"更深5m/1h支撑77.85外侧","basis_type":"structural"}
    ],
    "drawdown_rules":[
      {"timeframe":"5m","min_profit_pct":0.35,"max_drawdown_pct":60,"close_ratio_pct":65,"basis_type":"structural","stage_name":"partial_profit_lock"},
      {"timeframe":"1h","min_profit_pct":0.9,"max_drawdown_pct":55,"close_ratio_pct":80,"basis_type":"structural","stage_name":"runner_extension"}
    ]
  }},
  {"symbol":"BTCUSDT","action":"open_short","protection_plan":{
    "mode":"single","stop_loss_pct":1.2,"take_profit_pct":2.4,"stop_loss_anchor":"structural","take_profit_anchor":"structural",
    "break_even_trigger_mode":"profit_pct","break_even_trigger_value":1.0,"break_even_offset_pct":0.15
  }}
]`

func TestRealSnapshotLadderPlan(t *testing.T) {
	ps := realProtectionSnapshotFromDecisionJSON([]string{sampleDecisionJSON}, "SOLUSDT", "open_long")
	if ps == nil {
		t.Fatal("expected real plan for SOLUSDT open_long, got nil")
	}
	if ps.LadderTPSL == nil || len(ps.LadderTPSL.Rules) != 2 {
		t.Fatalf("expected 2 ladder rules, got %+v", ps.LadderTPSL)
	}
	r0 := ps.LadderTPSL.Rules[0]
	if r0.StopLossPrice != 77.95 || r0.StopLossCloseRatioPct != 75 {
		t.Errorf("tier0 real values not preserved: %+v", r0)
	}
	if r0.StructuralAnchor == "" || r0.VolatilityBufferPct != 0.17 {
		t.Errorf("tier0 structural/ATR provenance lost: %+v", r0)
	}
	if !ps.LadderTPSL.StopLossEnabled {
		t.Error("stop-loss should be enabled when rules carry SL")
	}
	if len(ps.Drawdown) != 2 {
		t.Fatalf("expected 2 drawdown stages, got %d", len(ps.Drawdown))
	}
	if ps.Drawdown[0].MinProfitPct != 0.35 || ps.Drawdown[0].CloseRatioPct != 65 {
		t.Errorf("drawdown stage0 real values lost: %+v", ps.Drawdown[0])
	}
}

func TestRealSnapshotFullTPSLAndBreakEven(t *testing.T) {
	ps := realProtectionSnapshotFromDecisionJSON([]string{sampleDecisionJSON}, "BTCUSDT", "open_short")
	if ps == nil {
		t.Fatal("expected real plan for BTCUSDT open_short, got nil")
	}
	if ps.FullTPSL == nil || ps.FullTPSL.StopLoss.Value != 1.2 || ps.FullTPSL.TakeProfit.Value != 2.4 {
		t.Errorf("full TP/SL real values lost: %+v", ps.FullTPSL)
	}
	if ps.BreakEven == nil || ps.BreakEven.TriggerValue != 1.0 || ps.BreakEven.OffsetPct != 0.15 {
		t.Errorf("break-even real values lost: %+v", ps.BreakEven)
	}
}

func TestRealSnapshotNilWhenNoPlan_NoTemplateFallback(t *testing.T) {
	// Legacy position: no protection_plan present for the match → must be nil,
	// never a template.
	legacy := `[{"symbol":"ETHUSDT","action":"open_long"}]`
	if ps := realProtectionSnapshotFromDecisionJSON([]string{legacy}, "ETHUSDT", "open_long"); ps != nil {
		t.Errorf("expected nil (no real plan → show 'not recorded'), got %+v", ps)
	}
	// Symbol mismatch → nil.
	if ps := realProtectionSnapshotFromDecisionJSON([]string{sampleDecisionJSON}, "XRPUSDT", "open_long"); ps != nil {
		t.Errorf("expected nil for unmatched symbol, got %+v", ps)
	}
}
