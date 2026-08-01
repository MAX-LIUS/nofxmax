"""Removes the tautology from probation.py.

probation.py showed AUC 0.68->0.88 for early path vs final outcome. That is
circular: the outcome is DEFINED by the path (fav>=0.5R arms break-even so the
trade cannot lose; adv>=1.0R stops it). Predicting a quantity from its own
definition is not information.

Honest version: restrict to trades that at bar k are STILL ALIVE and STILL
UNARMED -- nothing about their outcome is yet mechanically determined -- and ask
whether the path up to bar k predicts what happens FROM bar k ONWARD.

If yes: an early cut is real information and a probation gate is justified.
If no: the live winner/loser split is pure hindsight and there is no lever here
either, which must be reported as such.
"""
import numpy as np, pandas as pd
from scipy import stats
import bars, events as EV, evalgate as E

MAX_HOLD = 48
COST_BPS = 14.0
STOP_ATR = 1.5
ARM_R = 0.5
GIVE_R = 1.2
CHECK_BARS = [3, 4, 6, 8]


def sim_from(f, idx, atr, side):
    """Full simulation that records, per bar, the state and the eventual outcome
    of the REMAINDER measured from that bar's close."""
    o = f["open"].to_numpy(); h = f["high"].to_numpy()
    l = f["low"].to_numpy(); c = f["close"].to_numpy()
    n = len(o)
    ent = idx + 1
    ok = ent < n
    entry = np.where(ok, o[np.minimum(ent, n - 1)], np.nan)
    a = atr[idx]
    fh = EV._windows(h, ent, MAX_HOLD)
    fl = EV._windows(l, ent, MAX_HOLD)
    fc = EV._windows(c, ent, MAX_HOLD)
    if side == "LONG":
        fav = (fh - entry[:, None]) / a[:, None]
        adv = (entry[:, None] - fl) / a[:, None]
        clo = (fc - entry[:, None]) / a[:, None]
    else:
        fav = (entry[:, None] - fl) / a[:, None]
        adv = (fh - entry[:, None]) / a[:, None]
        clo = (entry[:, None] - fc) / a[:, None]

    r_unit_bps = (STOP_ATR * a / entry) * 10000.0
    cost_R = COST_BPS / np.maximum(r_unit_bps, 1e-9)
    m = len(idx)

    K = max(CHECK_BARS)
    alive = np.zeros((m, K), bool)      # still open AND still unarmed at bar k
    fav_k = np.full((m, K), np.nan)     # running fav (ATR) at bar k
    adv_k = np.full((m, K), np.nan)
    clo_k = np.full((m, K), np.nan)     # close pnl in R at bar k
    final = np.full(m, np.nan)          # full-hold outcome in R (net)

    for kk in range(m):
        if not ok[kk] or not np.isfinite(a[kk]) or a[kk] <= 0:
            continue
        floor, peak, armed = -1.0, 0.0, False
        rf, ra = 0.0, 0.0
        j = 0; done = False
        for j in range(MAX_HOLD):
            if not np.isfinite(clo[kk, j]):
                break
            fR = fav[kk, j] / STOP_ATR
            aR = adv[kk, j] / STOP_ATR
            rf = max(rf, fav[kk, j]); ra = max(ra, adv[kk, j])
            # adverse-first
            if -aR <= floor:
                final[kk] = floor - cost_R[kk]; done = True
                if j < K:
                    pass
                break
            peak = max(peak, fR)
            was_armed = armed
            if (not armed) and peak >= ARM_R:
                armed = True; floor = 0.0
            if armed and peak - GIVE_R > floor:
                floor = peak - GIVE_R
            if j < K:
                # state AFTER bar j resolves: alive, and unarmed as of entering
                # this bar (was_armed) so no outcome is yet locked in
                alive[kk, j] = (not was_armed)
                fav_k[kk, j] = rf
                adv_k[kk, j] = ra
                clo_k[kk, j] = clo[kk, j] / STOP_ATR
        if not done:
            final[kk] = clo[kk, j] / STOP_ATR - cost_R[kk]
    return alive, fav_k, adv_k, clo_k, final, cost_R


def run():
    syms = EV.all_symbols()
    acc = []
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
        atrp = feats["atr_pct"][idx]
        keep = np.isfinite(atrp) & (atrp >= 0.20)
        t = pd.PeriodIndex(pd.to_datetime(f.index.to_numpy()[idx], utc=True),
                           freq="Q").astype(str).to_numpy()
        for side in ("LONG", "SHORT"):
            al, fk, ak, ck, fin, cR = sim_from(f, idx, atr, side)
            d = {"q": t, "side": side, "final": fin, "costR": cR}
            for k in CHECK_BARS:
                d[f"alive{k}"] = al[:, k - 1]
                d[f"fav{k}"] = fk[:, k - 1]
                d[f"adv{k}"] = ak[:, k - 1]
                d[f"clo{k}"] = ck[:, k - 1]
            acc.append(pd.DataFrame(d)[keep].dropna(subset=["final"]))
        if (i + 1) % 15 == 0:
            print(f"  [{i+1}/{len(syms)}]", flush=True)

    e = pd.concat(acc, ignore_index=True)
    print(f"\nevents={len(e)}  quarters={e.q.nunique()}")

    print("\n=== conditioned on ALIVE & UNARMED at bar k ===")
    print("    (nothing about the outcome is mechanically determined yet)")
    for k in CHECK_BARS:
        s = e[e[f"alive{k}"]].copy()
        if len(s) < 500:
            print(f"  bar {k}: n={len(s)} too few"); continue
        # remaining outcome from bar k onward, in R, excluding cost already sunk
        s["rem"] = s.final - s[f"clo{k}"]
        y = (s.rem > 0).astype(int).to_numpy()
        print(f"\n  bar {k}: n={len(s)}  survivors={100*len(s)/len(e):.1f}%"
              f"  mean_remaining={s.rem.mean():+.4f}R  win={100*y.mean():.1f}%")
        for col in (f"fav{k}", f"adv{k}"):
            v = s[col].to_numpy(); mm = np.isfinite(v)
            if (y[mm] == 1).sum() == 0 or (y[mm] == 0).sum() == 0:
                continue
            auc = stats.mannwhitneyu(v[mm][y[mm] == 1], v[mm][y[mm] == 0],
                                     alternative="two-sided").statistic
            auc /= (y[mm] == 1).sum() * (y[mm] == 0).sum()
            print(f"      {col:<7} AUC={auc:.4f}  (>0.5 = higher is better)")
        # does cutting the worst tercile of adv beat holding everything?
        thr = s[f"adv{k}"].quantile(0.75)
        cut = s[s[f"adv{k}"] >= thr]
        hold = s[s[f"adv{k}"] < thr]
        print(f"      worst-quartile adv>={thr:.2f}ATR: mean_rem={cut.rem.mean():+.4f}R"
              f"   rest={hold.rem.mean():+.4f}R")
        dq = (cut.groupby(cut.q).rem.mean() - hold.groupby(hold.q).rem.mean()).dropna()
        fr, p, _, _ = E.sign_test(dq)
        print(f"      per-quarter delta={dq.mean():+.4f}R fracpos={fr:.2f} p={p:.4f}")

    e.to_parquet("cache/probation2.parquet", index=False)
    print("\nsaved cache/probation2.parquet")


if __name__ == "__main__":
    run()
