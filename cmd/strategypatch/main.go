package main

// strategypatch: DRY-RUN verifier. Applies the PROP_E protection patch to a
// strategy config backup using the SAME deep-merge semantics as the API
// (api/strategy.go deepMergeMap: nested maps recurse, arrays replace), parses
// the result into store.StrategyConfig, prints resulting rules, and writes the
// merged config for the later PUT. Does NOT touch the DB or live system.

import (
	"encoding/json"
	"fmt"
	"os"

	"nofx/store"
)

func deepMergeMap(dst, src map[string]any) {
	for key, srcVal := range src {
		srcMap, srcIsMap := srcVal.(map[string]any)
		dstMap, dstIsMap := dst[key].(map[string]any)
		if srcIsMap && dstIsMap {
			deepMergeMap(dstMap, srcMap)
			dst[key] = dstMap
			continue
		}
		dst[key] = srcVal
	}
}

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: strategypatch <existing-config.json> <patch.json>")
		os.Exit(1)
	}
	existingBlob, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	patchBlob, err := os.ReadFile(os.Args[2])
	if err != nil {
		panic(err)
	}

	var existingCfg store.StrategyConfig
	if err := json.Unmarshal(existingBlob, &existingCfg); err != nil {
		fmt.Printf("EXISTING PARSE ERROR: %v\n", err)
		os.Exit(1)
	}
	var existingMap map[string]any
	reblob, _ := json.Marshal(existingCfg)
	_ = json.Unmarshal(reblob, &existingMap)

	var patchMap map[string]any
	if err := json.Unmarshal(patchBlob, &patchMap); err != nil {
		fmt.Printf("PATCH PARSE ERROR: %v\n", err)
		os.Exit(1)
	}

	deepMergeMap(existingMap, patchMap)

	mergedBlob, _ := json.Marshal(existingMap)
	var merged store.StrategyConfig
	if err := json.Unmarshal(mergedBlob, &merged); err != nil {
		fmt.Printf("MERGED PARSE ERROR: %v\n", err)
		os.Exit(1)
	}

	p := merged.Protection
	fmt.Println("=== MERGED protection ===")
	fmt.Printf("ladder_tp_sl: enabled=%v mode=%s rules=%d\n",
		p.LadderTPSL.Enabled, p.LadderTPSL.Mode, len(p.LadderTPSL.Rules))
	for i, r := range p.LadderTPSL.Rules {
		fmt.Printf("  [%d] TP=%.2f(%s) tpRatio=%.0f%% SL=%.2f(%s) slRatio=%.0f%%\n",
			i, r.TakeProfitPct, r.TakeProfitUnit, r.TakeProfitCloseRatioPct,
			r.StopLossPct, r.StopLossUnit, r.StopLossCloseRatioPct)
	}
	fmt.Printf("break_even_stop: enabled=%v mode=%s rules=%d\n",
		p.BreakEvenStop.Enabled, p.BreakEvenStop.Mode, len(p.BreakEvenStop.Rules))
	for i, r := range p.BreakEvenStop.Rules {
		fmt.Printf("  [%d] trigger=%.2f(%s) offset=%.2f ratio=%.0f%% stage=%s\n",
			i, r.TriggerValue, r.TriggerUnit, r.OffsetPct, r.CloseRatioPct, r.StageName)
	}
	fmt.Printf("drawdown_take_profit: enabled=%v mode=%s engine=%s runnerKeep=%.0f firstReduce=%.0f rules=%d\n",
		p.DrawdownTakeProfit.Enabled, p.DrawdownTakeProfit.Mode, p.DrawdownTakeProfit.EngineMode,
		p.DrawdownTakeProfit.MinRunnerKeepPct, p.DrawdownTakeProfit.MaxFirstReducePct,
		len(p.DrawdownTakeProfit.Rules))
	for i, r := range p.DrawdownTakeProfit.Rules {
		fmt.Printf("  [%d] minProfit=%.2f(%s) maxDD=%.0f(%s) ratio=%.0f%% stage=%s\n",
			i, r.MinProfitPct, r.MinProfitUnit, r.MaxDrawdownPct, r.MaxDrawdownUnit,
			r.CloseRatioPct, r.StageName)
	}
	fmt.Printf("atr_protection: enabled=%v tf=%s minEff=%.2f maxEff=%.2f\n",
		merged.ATRProtection.Enabled, merged.ATRProtection.Timeframe,
		merged.ATRProtection.MinEffPct, merged.ATRProtection.MaxEffPct)

	outPath := os.Args[1] + ".merged"
	_ = os.WriteFile(outPath, mergedBlob, 0o644)
	fmt.Printf("\nMERGED config written to %s (%d bytes)\n", outPath, len(mergedBlob))
}
