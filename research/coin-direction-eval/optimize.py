"""Second-stage optimization: how should the lean be USED?

The per-quarter evidence says the lean's regression coefficient is positive in
13/15 quarters (sign test p=0.0037) but a median-split binary tilt only wins
9/13 (p=0.133). A median split throws away the magnitude, and the informative
part of a market-timing read is the tail, not the middle. So compare usages,
all with HISTORY-ONLY thresholds (no in-quarter peeking):

  M0 始终多头            - the honest baseline for a long-biased book
  M1 中位数二分          - what we tested before
  M2 连续加权            - weight = historical percentile of the lean
  M3 仅极端(三分位)       - long in top tercile, short in bottom, flat in middle
  M4 仅极端 + 我们 slope 一致 - require both reads to agree
  M5 只做减仓不反向        - long at full size when bullish, half size when bearish
"""
from __future__ import annotations
import glob
from math import comb
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
RNG = np.random.default_rng(20260729)


def sign_p(k, n):
    return sum(comb(n, i) for i in range(k, n + 1)) / 2 ** n


def build_hourly():
    rows = []
    for p in sorted(glob.glob(f"{BASE}/wf_scored/*.pkl")):
        d = pd.read_pickle(p)
        d.index = pd.to_datetime(d.index, utc=True)
        d = d[np.isfinite(d.raw_up) & np.isfinite(d.net_long) & np.isfinite(d.net_short)]
        if d.empty:
            continue
        g = d.groupby(d.index)
        h = pd.DataFrame({"lean": g["raw_up"].mean(),
                          "our_share": (d.slope168 < 0).groupby(d.index).mean(),
                          "mL": g["net_long"].mean(),
                          "mS": g["net_short"].mean(),
                          "n": g.size()})
        rows.append(h[h.n >= 10])
        del d, g, h
    return pd.concat(rows).sort_index()


def block_ci(t, v, n_boot=3000, block_days=7):
    t = np.asarray(t, dtype="datetime64[ns]")
    blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // block_days)
    uniq = np.unique(blk)
    by = {b: v[blk == b] for b in uniq}
    s = np.empty(n_boot)
    for i in range(n_boot):
        pick = RNG.choice(uniq, len(uniq), replace=True)
        s[i] = np.concatenate([by[b] for b in pick]).mean()
    return (float(np.mean(v)), *np.percentile(s, [2.5, 97.5]))


def main():
    h = build_hourly()
    h["q"] = pd.PeriodIndex(h.index.tz_localize(None), freq="Q")
    qs = sorted(h.q.unique())

    per_q = {k: [] for k in ("M1", "M2", "M3", "M4", "M5")}
    base_q = []
    pooled = {k: [] for k in per_q}
    pooled_t, pooled_base = [], []
    qlabels = []

    for i in range(2, len(qs)):
        past, cur = h[h.q < qs[i]], h[h.q == qs[i]]
        if len(cur) < 60 or len(past) < 200:
            continue
        mL, mS = cur.mL.to_numpy(), cur.mS.to_numpy()
        lean, osh = cur.lean.to_numpy(), cur.our_share.to_numpy()
        pl = np.sort(past.lean.to_numpy())
        med = np.median(pl)
        t1, t2 = np.quantile(pl, [1 / 3, 2 / 3])
        o_med = past.our_share.median()

        # weight in [0,1]: 1 = fully long, 0 = fully short
        pct = np.searchsorted(pl, lean) / max(len(pl), 1)

        r = {}
        r["M1"] = np.where(lean > med, mL, mS)
        r["M2"] = pct * mL + (1 - pct) * mS
        r["M3"] = np.where(lean >= t2, mL, np.where(lean <= t1, mS, 0.0))
        agree_up = (lean >= t2) & (osh < o_med)
        agree_dn = (lean <= t1) & (osh >= o_med)
        r["M4"] = np.where(agree_up, mL, np.where(agree_dn, mS, 0.0))
        r["M5"] = np.where(lean > med, mL, 0.5 * mL)

        base = mL.mean()
        base_q.append(base)
        qlabels.append(str(qs[i]))
        tt = cur.index.tz_localize(None).to_numpy("datetime64[ns]")
        pooled_t.append(tt)
        pooled_base.append(mL)
        for k, v in r.items():
            per_q[k].append(v.mean() - base)
            pooled[k].append(v - mL)

    t = np.concatenate(pooled_t)
    print(f"有效季度 {len(qlabels)} ({qlabels[0]} ~ {qlabels[-1]}), "
          f"小时 {len(t)}\n")
    print(f"基准 始终多头: 逐季均值 {np.mean(base_q)*100:+.4f}%, "
          f">0 的季度 {int((np.array(base_q)>0).sum())}/{len(base_q)}")
    bm, blo, bhi = block_ci(t, np.concatenate(pooled_base))
    print(f"           小时级 {bm*100:+.4f}% CI [{blo*100:+.4f},{bhi*100:+.4f}]"
          f" {'显著为负' if bhi < 0 else ''}\n")

    names = {"M1": "中位数二分", "M2": "连续加权(历史分位)", "M3": "仅极端三分位",
             "M4": "极端+slope一致", "M5": "只减仓不反向"}
    print(f"{'方案':22s} {'季胜率':>7s} {'季均改善%':>10s} {'最差季%':>9s} "
          f"{'符号p':>7s} {'小时级改善%':>11s} {'95%CI':>22s}")
    rank = []
    for k in ("M1", "M2", "M3", "M4", "M5"):
        g = np.array(per_q[k])
        kk, n = int((g > 0).sum()), len(g)
        m, lo, hi = block_ci(t, np.concatenate(pooled[k]))
        star = "显著" if lo > 0 else ""
        print(f"{names[k]:22s} {kk:3d}/{n:<3d} {g.mean()*100:+10.4f} "
              f"{g.min()*100:+9.4f} {sign_p(kk,n):7.4f} {m*100:+11.4f} "
              f"[{lo*100:+.4f},{hi*100:+.4f}] {star}")
        rank.append((k, g.mean(), kk / n, lo, m))

    print("\n=== 逐季度明细 (改善 %) ===")
    print(f"{'季度':10s} {'始终多头':>9s} " + " ".join(f"{names[k][:8]:>9s}" for k in per_q))
    for j, q in enumerate(qlabels):
        print(f"{q:10s} {base_q[j]*100:+9.4f} " +
              " ".join(f"{per_q[k][j]*100:+9.4f}" for k in per_q))

    best = max(rank, key=lambda z: (z[3] > 0, z[2], z[1]))
    print(f"\n最优方案: {names[best[0]]}  (季胜率 {best[2]*100:.0f}%, "
          f"季均改善 {best[1]*100:+.4f}%, 小时级 {best[4]*100:+.4f}%, "
          f"CI下界 {best[3]*100:+.4f}%)")

    # what does the best arm's absolute return look like vs always-long
    k = best[0]
    abs_ret = np.mean(base_q) + best[1]
    print(f"  绝对口径: 始终多头 {np.mean(base_q)*100:+.4f}% -> "
          f"{names[k]} {abs_ret*100:+.4f}% (每笔每 72h 持仓, 已扣 10bps 成本)")


if __name__ == "__main__":
    main()
