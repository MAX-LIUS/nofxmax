package api

import (
	"os"
	"testing"
	"time"

	"nofx/store"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	glog "gorm.io/gorm/logger"
)

// Exercises getEquityHistoryForTraders end to end against a fixture built from a
// production subset. Set NOFX_EQUITY_FIXTURE to the fixture path to run it; the test
// skips otherwise so CI does not depend on a local file.
//
// Never point this at the production database: Store construction paths AutoMigrate.
func TestEquityHistoryBatchOverRealShapedData(t *testing.T) {
	fixture := os.Getenv("NOFX_EQUITY_FIXTURE")
	if fixture == "" {
		t.Skip("set NOFX_EQUITY_FIXTURE to a fixture DB path to run")
	}
	gdb, err := gorm.Open(sqlite.Open(fixture), &gorm.Config{Logger: glog.Default.LogMode(glog.Silent)})
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	st, err := store.NewFromGorm(gdb)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	// traderManager stays nil-valued: GetTrader on an empty manager just errors, which
	// is the "trader not in memory" path, so no live-tail point is appended.
	s := &Server{store: st}

	traderID := os.Getenv("NOFX_EQUITY_TRADER")
	if traderID == "" {
		traderID = "4801de05_f40c31f0-d7c4-4ea6-ad76-a5f2b56dc065_claude_1779550392"
	}

	t.Run("all history spans far more than the old 500-row tail", func(t *testing.T) {
		res := s.getEquityHistoryForTraders([]string{traderID}, 0, 1500)
		hist := res["histories"].(map[string]interface{})[traderID].([]map[string]interface{})
		if len(hist) == 0 {
			t.Fatal("no history returned; is the fixture populated for this trader?")
		}
		first := hist[0]["timestamp"].(time.Time)
		last := hist[len(hist)-1]["timestamp"].(time.Time)
		days := last.Sub(first).Hours() / 24
		if days < 30 {
			t.Fatalf("hours=0 must return the whole history, got only %.1f days", days)
		}
		if len(hist) > 1520 {
			t.Fatalf("budget 1500 exceeded: %d points", len(hist))
		}
		samp := res["sampling"].(map[string]interface{})[traderID].(map[string]interface{})
		if samp["raw_points"].(int) <= len(hist) && samp["downsampled"].(bool) {
			t.Errorf("sampling metadata inconsistent: %v", samp)
		}
	})

	t.Run("range buttons return different spans", func(t *testing.T) {
		spans := map[int]float64{}
		for _, h := range []int{24, 72, 168, 720} {
			res := s.getEquityHistoryForTraders([]string{traderID}, h, 1500)
			hist := res["histories"].(map[string]interface{})[traderID].([]map[string]interface{})
			if len(hist) < 2 {
				t.Fatalf("hours=%d returned %d points", h, len(hist))
			}
			first := hist[0]["timestamp"].(time.Time)
			last := hist[len(hist)-1]["timestamp"].(time.Time)
			spans[h] = last.Sub(first).Hours()
		}
		// This is the reported defect: every range button rendered roughly the same
		// window. Each longer button must actually cover more time.
		if !(spans[24] < spans[72] && spans[72] < spans[168] && spans[168] < spans[720]) {
			t.Fatalf("spans not increasing with the requested window: %v", spans)
		}
	})

	t.Run("points carry position count and notional", func(t *testing.T) {
		res := s.getEquityHistoryForTraders([]string{traderID}, 0, 400)
		hist := res["histories"].(map[string]interface{})[traderID].([]map[string]interface{})
		withNotional, withCount, agree, compared := 0, 0, 0, 0
		for _, p := range hist {
			if _, ok := p["position_count"]; ok {
				withCount++
			}
			if n, ok := p["position_notional"].(float64); ok && n > 0 {
				withNotional++
			}
			rec, okR := p["position_count"].(int)
			recon, okC := p["position_count_recon"].(int)
			if okR && okC {
				compared++
				if rec == recon {
					agree++
				}
			}
		}
		if withCount != len(hist) {
			t.Errorf("position_count missing on %d of %d points", len(hist)-withCount, len(hist))
		}
		if withNotional == 0 {
			t.Error("no point carried a non-zero position_notional")
		}
		if compared == 0 {
			t.Fatal("reconstructed count never present, cannot cross-check")
		}
		// Not asserting exact equality: the recorded count comes from the exchange at
		// that instant, the reconstructed one from local rows, and older history has
		// genuine bookkeeping drift. Agreement well below this would mean the replay
		// is wrong rather than that the books drifted.
		ratio := float64(agree) / float64(compared)
		if ratio < 0.80 {
			t.Errorf("reconstructed count agrees on only %.1f%% of points, replay likely wrong", ratio*100)
		}
		t.Logf("count agreement %.1f%% over %d points, %d with notional", ratio*100, compared, withNotional)
	})

	t.Run("downsampling keeps the real peak and trough", func(t *testing.T) {
		full := s.getEquityHistoryForTraders([]string{traderID}, 0, 100000)
		reduced := s.getEquityHistoryForTraders([]string{traderID}, 0, 300)
		peak := func(hist []map[string]interface{}) (float64, float64) {
			hi, lo := -1e18, 1e18
			for _, p := range hist {
				v := p["total_equity"].(float64)
				if v > hi {
					hi = v
				}
				if v < lo {
					lo = v
				}
			}
			return hi, lo
		}
		fh, fl := peak(full["histories"].(map[string]interface{})[traderID].([]map[string]interface{}))
		rh, rl := peak(reduced["histories"].(map[string]interface{})[traderID].([]map[string]interface{}))
		if fh != rh {
			t.Errorf("peak lost by downsampling: full %.2f vs reduced %.2f", fh, rh)
		}
		if fl != rl {
			t.Errorf("trough lost by downsampling: full %.2f vs reduced %.2f", fl, rl)
		}
		t.Logf("peak %.2f trough %.2f preserved at 300-point budget", rh, rl)
	})
}
