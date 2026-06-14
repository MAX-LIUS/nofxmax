package store

import (
	"math"
	"testing"
)

// TestFillExpectancyMetrics verifies the net win-rate / payoff / expectancy / fee-drag math
// against a hand-computed fixture inserted directly into trader_positions.
func TestFillExpectancyMetrics(t *testing.T) {
	db := openDecisionTestDB(t)
	ds := NewDecisionStore(db)

	// Minimal trader_positions table (only the columns the query reads + status filter).
	if err := db.Exec(`CREATE TABLE trader_positions (
		id INTEGER PRIMARY KEY,
		trader_id TEXT,
		status TEXT,
		realized_pnl REAL,
		fee REAL
	)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Fixture: 2 net winners, 2 net losers, 1 OPEN (ignored).
	//  win1: gross 3.0 fee 0.5 -> net 2.5
	//  win2: gross 2.0 fee 0.5 -> net 1.5
	//  loss1: gross -1.0 fee 0.5 -> net -1.5
	//  loss2: gross -2.0 fee 0.5 -> net -2.5
	//  open: ignored
	rows := []struct {
		status string
		gross  float64
		fee    float64
	}{
		{"CLOSED", 3.0, 0.5},
		{"CLOSED", 2.0, 0.5},
		{"CLOSED", -1.0, 0.5},
		{"CLOSED", -2.0, 0.5},
		{"OPEN", 99.0, 0.0},
	}
	for _, r := range rows {
		if err := db.Exec(`INSERT INTO trader_positions (trader_id,status,realized_pnl,fee) VALUES (?,?,?,?)`,
			"t1", r.status, r.gross, r.fee).Error; err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	stats := &Statistics{}
	ds.fillExpectancyMetrics(stats, "trader_id = ?", "t1")

	if stats.ClosedTrades != 4 {
		t.Errorf("ClosedTrades=%d want 4", stats.ClosedTrades)
	}
	if stats.NetWins != 2 {
		t.Errorf("NetWins=%d want 2", stats.NetWins)
	}
	if math.Abs(stats.NetWinRate-0.5) > 1e-9 {
		t.Errorf("NetWinRate=%.4f want 0.5", stats.NetWinRate)
	}
	// gross sum = 3+2-1-2 = 2.0 ; fees = 2.0 ; net = 0.0
	if math.Abs(stats.GrossPnLUSD-2.0) > 1e-9 {
		t.Errorf("GrossPnLUSD=%.4f want 2.0", stats.GrossPnLUSD)
	}
	if math.Abs(stats.TotalFeesUSD-2.0) > 1e-9 {
		t.Errorf("TotalFeesUSD=%.4f want 2.0", stats.TotalFeesUSD)
	}
	if math.Abs(stats.NetPnLUSD-0.0) > 1e-9 {
		t.Errorf("NetPnLUSD=%.4f want 0.0", stats.NetPnLUSD)
	}
	// avg win = (2.5+1.5)/2 = 2.0 ; avg loss = (-1.5-2.5)/2 = -2.0 ; payoff = 1.0
	if math.Abs(stats.AvgWinUSD-2.0) > 1e-9 {
		t.Errorf("AvgWinUSD=%.4f want 2.0", stats.AvgWinUSD)
	}
	if math.Abs(stats.AvgLossUSD-(-2.0)) > 1e-9 {
		t.Errorf("AvgLossUSD=%.4f want -2.0", stats.AvgLossUSD)
	}
	if math.Abs(stats.PayoffRatio-1.0) > 1e-9 {
		t.Errorf("PayoffRatio=%.4f want 1.0", stats.PayoffRatio)
	}
	// expectancy = net/n = 0/4 = 0 -> marginal
	if math.Abs(stats.ExpectancyUSD-0.0) > 1e-9 {
		t.Errorf("ExpectancyUSD=%.4f want 0.0", stats.ExpectancyUSD)
	}
	if stats.HealthFlag != "marginal" {
		t.Errorf("HealthFlag=%q want marginal", stats.HealthFlag)
	}
	// fee drag = fees/|gross| = 2.0/2.0 = 1.0
	if math.Abs(stats.FeeDragRatio-1.0) > 1e-9 {
		t.Errorf("FeeDragRatio=%.4f want 1.0", stats.FeeDragRatio)
	}
}

// TestFillExpectancyMetricsNegative checks the negative-expectancy health flag path.
func TestFillExpectancyMetricsNegative(t *testing.T) {
	db := openDecisionTestDB(t)
	ds := NewDecisionStore(db)
	if err := db.Exec(`CREATE TABLE trader_positions (id INTEGER PRIMARY KEY, trader_id TEXT, status TEXT, realized_pnl REAL, fee REAL)`).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	db.Exec(`INSERT INTO trader_positions (trader_id,status,realized_pnl,fee) VALUES ('t1','CLOSED',1.0,0.5)`)
	db.Exec(`INSERT INTO trader_positions (trader_id,status,realized_pnl,fee) VALUES ('t1','CLOSED',-3.0,0.5)`)
	stats := &Statistics{}
	ds.fillExpectancyMetrics(stats, "trader_id = ?", "t1")
	if stats.HealthFlag != "negative" {
		t.Errorf("HealthFlag=%q want negative", stats.HealthFlag)
	}
	if stats.ExpectancyUSD >= 0 {
		t.Errorf("ExpectancyUSD=%.4f want negative", stats.ExpectancyUSD)
	}
}
