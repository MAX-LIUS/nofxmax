"""Decisive test: does net R keep improving as the stop widens without bound?

If yes -> the gain is the 1/stop_width friction curve, no optimum exists, and
"widen the stop" is arithmetic, not a market finding.
If it turns -> there is a real geometry optimum worth acting on.

Also tracks the MECHANISM: arm rate, stop-out rate, timeout rate.
"""
import numpy as np, pandas as pd
import bars, events as EV, evalgate as E
import geometry as G

MAX_HOLD = 48
COST_BPS = 14.0
WIDTHS = [1.0, 1.5, 2.0, 2.5, 3.0, 4.0, 5.0, 6.0, 8.0]


def sim_diag(f, idx, atr, side, stop_atr, arm_at_R=0.50,
             tp1_R=0.80, tp1_frac=0.30, give_R=1.20):
    """Same engine as geometry.sim but also returns exit-reason diagnostics."""
    o = f["open"].to_numpy(); h = f["high"].to_numpy()
    l = f["low"].to_numpy(); c = f["close"].to_numpy()
    n = len(o)
    ent = idx + 1
    ok = ent < n
    entry = np.where(ok, o[np.minimum(ent, n - 1)], np.nan)
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
    armed_f = np.zeros(m); stopped_f = np.zeros(m); timeout_f = np.zeros(m)
    r_unit_bps = (risk / entry) * 10000.0
    cost_R = COST_BPS / np.maximum(r_unit_bps, 1e-9)
    for k in range(m):
        if not ok[k] or not np.isfinite(risk[k]) or risk[k] <= 0:
            continue
        pos, realized, peak = 1.0, 0.0, 0.0
        armed = tp1_done = done = False
        floor = -1.0
        j = 0
        for j in range(MAX_HOLD):
            if not np.isfinite(clo[k, j]):
                break
            a_, f_ = adv[k, j], fav[k, j]
            if -a_ <= floor:
                realized += pos * floor
                pos = 0.0; done = True
                if floor <= -1.0 + 1e-12:
                    stopped_f[k] = 1.0
                break
            if (not tp1_done) and f_ >= tp1_R:
                realized += tp1_frac * tp1_R
                pos -= tp1_frac; tp1_done = True
            if f_ > peak:
                peak = f_
            if (not armed) and peak >= arm_at_R:
                armed = True; floor = 0.0
            if armed and give_R > 0 and peak - give_R > floor:
                floor = peak - give_R
        if not done and pos > 0:
            realized += pos * clo[k, j]
            timeout_f[k] = 1.0
        armed_f[k] = 1.0 if armed else 0.0
        out[k] = realized - cost_R[k]
    return out, armed_f, stopped_f, timeout_f, cost_R


def run():
    syms = EV.all_symbols()
    acc = {w: [] for w in WIDTHS}
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
        keep = np.isfinite(atrp) & (atrp >= 0.20)
        for w in WIDTHS:
            for side in ("LONG", "SHORT"):
                v, am, st, to, cr = sim_diag(f, idx, atr, side, w)
                acc[w].append(pd.DataFrame(
                    {"t": t, "R": v, "armed": am, "stopped": st,
                     "timeout": to, "costR": cr})[keep])
        if (i + 1) % 15 == 0:
            print(f"  [{i+1}/{len(syms)}]", flush=True)

    rows = []
    per_q = {}
    for w, lst in acc.items():
        d = pd.concat(lst, ignore_index=True).dropna(subset=["R"])
        d["q"] = pd.PeriodIndex(pd.to_datetime(d.t, utc=True), freq="Q").astype(str)
        q = d.groupby("q").R.mean()
        per_q[w] = q
        rows.append(dict(stop_atr=w, qmeanR=q.mean(), meanR=d.R.mean(),
                         win=100*(d.R > 0).mean(),
                         arm_pct=100*d.armed.mean(),
                         stop_pct=100*d.stopped.mean(),
                         timeout_pct=100*d.timeout.mean(),
                         mean_costR=d.costR.mean()))
    t = pd.DataFrame(rows).set_index("stop_atr")
    print("\n=== stop width sweep: is there an interior optimum? ===")
    print(t.round(4).to_string())

    print("\n=== is net R still improving at the wide end? (paired quarters) ===")
    seq = sorted(WIDTHS)
    for a, b in zip(seq, seq[1:]):
        d = (per_q[b] - per_q[a]).dropna()
        fr, p, _, _ = E.sign_test(d)
        print(f"  {a:>4} -> {b:<4}  delta={d.mean():+.4f}  fracpos={fr:.2f}  p={p:.4f}")

    print("\n=== R excluding friction entirely (gross), same sweep ===")
    print("  (if gross is flat/declining while net rises, it is pure cost arithmetic)")
    for w in seq:
        d = pd.concat(acc[w], ignore_index=True).dropna(subset=["R"])
        d["q"] = pd.PeriodIndex(pd.to_datetime(d.t, utc=True), freq="Q").astype(str)
        gross = (d.R + d.costR)
        qg = gross.groupby(d.q).mean()
        print(f"  stop {w:>4}  grossR={qg.mean():+.4f}  netR={per_q[w].mean():+.4f}"
              f"  costR={d.costR.mean():.4f}")


if __name__ == "__main__":
    run()
