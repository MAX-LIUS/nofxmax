"""The lever the evidence actually supports: stop/arm geometry, not entry filtering.

REASONING CHAIN
===============
1. Live book: 62% win rate, break-even stop returns +65bps on 318 trades — the
   protection stack works ONCE ARMED.
2. All the loss comes from 155 trades (18.7%) that went adverse before arming,
   averaging -220bps.
3. Predicting WHICH trades those will be is out of reach: out-of-sample AUC 0.503
   over 16 quarters (score.py), consistent with the 0.4999 ceiling the PMG study
   established for decision-time path features.
4. Therefore the remaining lever is not selection but geometry: how far the stop
   sits, and how early break-even arms.

Under risk-budget sizing a tighter stop buys a bigger position, so R is held
constant by construction and the comparison is honest: only the win/loss MIX
changes, not the bet size.

Grid: stop distance x arm threshold, evaluated per quarter with a sign test.
"""
import numpy as np, pandas as pd
from scipy import stats
import bars, events as EV
import evalgate as E

MAX_HOLD = 48
COST_BPS = 14.0


def sim(f, idx, atr, side, stop_atr, arm_at_R, tp1_R, tp1_frac, give_R):
    o = f["open"].to_numpy(); h = f["high"].to_numpy()
    l = f["low"].to_numpy(); c = f["close"].to_numpy()
    n = len(o)
    ent = idx + 1
    ok = ent < n
    entry = np.where(ok, o[np.minimum(ent, n-1)], np.nan)
    risk = stop_atr * atr[idx]
    fh = EV._windows(h, ent, MAX_HOLD); fl = EV._windows(l, ent, MAX_HOLD)
    fc = EV._windows(c, ent, MAX_HOLD)
    if side == "LONG":
        fav = (fh - entry[:, None]) / risk[:, None]
        adv = (entry[:, None] - fl) / risk[:, None]
        clo = (fc - entry[:, None]) / risk[:, None]
    else:
        fav = (entry[:, None] - fl) / risk[:, None]
        adv = (fh - entry[:, None]) / risk[:, None]
        clo = (entry[:, None] - fc) / risk[:, None]
    m = len(idx)
    out = np.full(m, np.nan)
    # Cost in R units = round-trip bps / R-unit width in bps. The economic floor
    # is applied ONCE in run() on atr_pct, config-independently, so every config
    # is scored on the identical sample and the paired quarter test stays valid.
    r_unit_bps = (risk / entry) * 10000.0
    cost_R = COST_BPS / np.maximum(r_unit_bps, 1e-9)
    for k in range(m):
        if not ok[k] or not np.isfinite(risk[k]) or risk[k] <= 0:
            continue
        pos, realized, peak = 1.0, 0.0, 0.0
        armed, tp1_done, done = False, False, False
        floor = -1.0
        j = 0
        for j in range(MAX_HOLD):
            if not np.isfinite(clo[k, j]):
                break
            a_, f_ = adv[k, j], fav[k, j]
            # adverse first
            if -a_ <= floor:
                realized += pos * floor
                pos = 0.0; done = True; break
            if (not tp1_done) and f_ >= tp1_R:
                realized += tp1_frac * tp1_R
                pos -= tp1_frac; tp1_done = True
            if f_ > peak:
                peak = f_
            if (not armed) and peak >= arm_at_R:
                armed = True
                floor = 0.0            # break-even
            if armed and give_R > 0 and peak - give_R > floor:
                floor = peak - give_R  # ratchet
        if not done and pos > 0:
            realized += pos * clo[k, j]
        out[k] = realized - cost_R[k]
    return out


GRID = [
    # (stop_atr, arm_at_R, tp1_R, tp1_frac, give_R, label)
    (1.5, 0.50, 0.80, 0.30, 1.20, "live-ish (stop1.5 arm0.5)"),
    (1.5, 0.35, 0.80, 0.30, 1.20, "arm earlier 0.35"),
    (1.5, 0.75, 0.80, 0.30, 1.20, "arm later 0.75"),
    (1.0, 0.50, 0.80, 0.30, 1.20, "stop 1.0 ATR"),
    (2.0, 0.50, 0.80, 0.30, 1.20, "stop 2.0 ATR"),
    (2.5, 0.50, 0.80, 0.30, 1.20, "stop 2.5 ATR"),
    (2.0, 0.35, 0.80, 0.30, 1.20, "stop 2.0 + arm 0.35"),
    (2.5, 0.35, 0.80, 0.30, 1.20, "stop 2.5 + arm 0.35"),
    (2.0, 0.35, 0.60, 0.30, 1.20, "stop 2.0 arm .35 tp1 .6"),
    (2.0, 0.35, 0.80, 0.50, 1.20, "stop 2.0 arm .35 tp1 50%"),
]


def run():
    syms = EV.all_symbols()
    res = {lab: [] for *_, lab in GRID}
    meta = []
    for i, s in enumerate(syms):
        f = bars.load_symbol(s)
        if f is None or len(f) < 400:
            continue
        feats = EV.build_features(f)
        atr = feats.pop("_atr")
        n = len(f)
        idx = np.arange(200, n - MAX_HOLD - 2, EV.STRIDE * 4)
        if len(idx) == 0:
            continue
        t = f.index.to_numpy()[idx]
        atrp = feats["atr_pct"][idx]
        # atr_pct is in percent; 1 ATR = atr_pct*100 bps. Require the TIGHTEST
        # config (1.0 ATR) to still clear ~1.4x round-trip cost => atr_pct >= 0.20.
        keep = np.isfinite(atrp) & (atrp >= 0.20)
        for stop_atr, armR, tp1R, tp1f, giveR, lab in GRID:
            for side in ("LONG","SHORT"):
                v = sim(f, idx, atr, side, stop_atr, armR, tp1R, tp1f, giveR)
                res[lab].append(pd.DataFrame({"t": t, "side": side, "R": v})[keep])
        meta.append(s)
        if (i+1) % 15 == 0:
            print(f"  [{i+1}/{len(syms)}]", flush=True)

    rows = []
    per_q = {}
    for lab in res:
        d = pd.concat(res[lab], ignore_index=True).dropna(subset=["R"])
        d["q"] = pd.PeriodIndex(pd.to_datetime(d.t, utc=True), freq="Q").astype(str)
        q = d.groupby("q").R.mean()
        per_q[lab] = q
        rows.append(dict(config=lab, n=len(d), meanR=d.R.mean(), qmeanR=q.mean(),
                         win=100*(d.R > 0).mean(), medR=d.R.median(),
                         p05=d.R.quantile(0.05), worst_q=q.min()))
    t = pd.DataFrame(rows).set_index("config").sort_values("qmeanR", ascending=False)
    print("\n=== geometry grid (quarter-equal-weighted R per trade) ===")
    print(t.round(4).to_string())

    base = per_q["live-ish (stop1.5 arm0.5)"]
    print("\n=== paired vs live-ish baseline, per quarter ===")
    out = []
    for lab, q in per_q.items():
        if lab == "live-ish (stop1.5 arm0.5)":
            continue
        d = (q - base).dropna()
        fr, p, npos, n = E.sign_test(d)
        out.append(dict(config=lab, delta_R=d.mean(), frac_pos=fr, binom_p=p,
                        worst=d.min(), nq=n))
    print(pd.DataFrame(out).set_index("config").sort_values("delta_R", ascending=False).round(4).to_string())


if __name__ == "__main__":
    run()
