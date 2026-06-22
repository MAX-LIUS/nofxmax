package trader

import "testing"

func TestParseAIMultiples_PlainJSON(t *testing.T) {
	resp := `{"stop_loss_atr":3.5,"take_profit_1_atr":2.5,"take_profit_2_atr":5.0,"break_even_1_atr":1.5,"break_even_2_atr":2.5,"reasoning":"mid vol"}`
	m, ok := parseAIMultiples(resp)
	if !ok || m.StopLossATR != 3.5 || m.TakeProfit2ATR != 5.0 || m.BreakEven1ATR != 1.5 {
		t.Fatalf("parse failed: %+v ok=%v", m, ok)
	}
}

func TestParseAIMultiples_WithFenceAndProse(t *testing.T) {
	resp := "Here you go:\n```json\n{\"stop_loss_atr\":4,\"take_profit_1_atr\":3,\"take_profit_2_atr\":6,\"break_even_1_atr\":2,\"break_even_2_atr\":3}\n```\nDone."
	m, ok := parseAIMultiples(resp)
	if !ok || m.StopLossATR != 4 || m.TakeProfit2ATR != 6 {
		t.Fatalf("fence parse failed: %+v ok=%v", m, ok)
	}
}

func TestParseAIMultiples_Garbage(t *testing.T) {
	if _, ok := parseAIMultiples("no json here"); ok {
		t.Fatalf("expected failure on garbage")
	}
}
