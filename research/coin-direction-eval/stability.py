"""Is the market-level lean stable enough to deploy, or is the pooled result
carried by a few periods?

A signal that is significant pooled but sign-flips in half the quarters is not
deployable: we would be trading the average of a coin flip. Tests:
  1. Per-quarter lean regression coefficient (same spec as the pooled one).
  2. Per-quarter tercile spread (模型看多 - 模型看空) of realized edge.
  3. Per-quarter tradable version: tilt the book by the lean, net of cost, and
     report hit rate across quarters.
  4. Rolling: does a coefficient fitted on past quarters predict the next one?
     This is the only test that matches how we would actually use it.
"""
from __future__ import annotations
import glob
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
RNG = np.random.default_rng(20260729)


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
        h["edge"] = h.mL - h.mS
        rows.append(h[h.n >= 10])
        del d, g, h
    return pd.concat(rows).sort_index()


def main():
    h = build_hourly()
    h["q"] = h.index.to_period("Q")
    print(f"面板 {len(h)} 小时, {h.q.nunique()} 个季度\n")

    print("=== 1. 逐季度: lean 回归系数 + 三分位 edge 价差 + 可交易口径 ===")
    print(f"{'季度':10s} {'小时':>5s} {'系数%':>9s} {'三分位价差%':>11s} "
          f"{'始终多头%':>9s} {'lean倾斜%':>9s} {'倾斜-多头':>9s}")
    recs = []
    for q, gq in h.groupby("q", observed=True):
        if len(gq) < 60:
            continue
        x = (gq.lean - gq.lean.mean()) / (gq.lean.std() + 1e-12)
        X = np.column_stack([x, np.ones(len(gq))])
        b, *_ = np.linalg.lstsq(X, gq.edge.to_numpy(), rcond=None)
        # tercile spread within the quarter (in-quarter cuts = generous to model)
        lo_c, hi_c = gq.lean.quantile([1 / 3, 2 / 3])
        sp = gq.edge[gq.lean >= hi_c].mean() - gq.edge[gq.lean <= lo_c].mean()
        # tradable: full-sample median cut fixed from PRIOR data only would be
        # ideal; use the quarter's own median here and flag it as optimistic
        cut = gq.lean.median()
        tilt = np.where(gq.lean.to_numpy() > cut, gq.mL.to_numpy(), gq.mS.to_numpy())
        recs.append((str(q), len(gq), b[0] * 100, sp * 100,
                     gq.mL.mean() * 100, tilt.mean() * 100,
                     (tilt.mean() - gq.mL.mean()) * 100))
        print(f"{recs[-1][0]:10s} {len(gq):5d} {b[0]*100:+9.4f} {sp*100:+11.4f} "
              f"{gq.mL.mean()*100:+9.4f} {tilt.mean()*100:+9.4f} "
              f"{(tilt.mean()-gq.mL.mean())*100:+9.4f}")

    r = pd.DataFrame(recs, columns=["q", "n", "coef", "spread", "always_long",
                                    "tilt", "gain"])
    print(f"\n  系数>0 的季度: {int((r.coef>0).sum())}/{len(r)}  "
          f"均值 {r.coef.mean():+.4f}%  中位 {r.coef.median():+.4f}%  "
          f"最差 {r.coef.min():+.4f}%")
    print(f"  三分位价差>0: {int((r.spread>0).sum())}/{len(r)}  均值 {r.spread.mean():+.4f}%")
    print(f"  倾斜优于始终多头: {int((r.gain>0).sum())}/{len(r)}  "
          f"均值 {r.gain.mean():+.4f}%  最差 {r.gain.min():+.4f}%")
    # sign test
    k, n = int((r.gain > 0).sum()), len(r)
    from math import comb
    pval = sum(comb(n, i) for i in range(k, n + 1)) / 2 ** n
    print(f"  符号检验 p = {pval:.4f} {'显著' if pval < 0.05 else '不显著'}")

    print("\n=== 2. 真正的实用检验: 用过去季度定阈值, 交易下一个季度 ===")
    print("    (前面用了季度自身中位数, 属于偷看; 这里改成只用历史)")
    qs = sorted(h.q.unique())
    hist_gain = []
    print(f"{'季度':10s} {'历史阈值':>10s} {'始终多头%':>9s} {'倾斜%':>9s} {'差%':>9s}")
    for i in range(2, len(qs)):
        past = h[h.q < qs[i]]
        cur = h[h.q == qs[i]]
        if len(cur) < 60 or len(past) < 200:
            continue
        cut = past.lean.median()
        tilt = np.where(cur.lean.to_numpy() > cut, cur.mL.to_numpy(), cur.mS.to_numpy())
        gain = (tilt.mean() - cur.mL.mean()) * 100
        hist_gain.append(gain)
        print(f"{str(qs[i]):10s} {cut:10.5f} {cur.mL.mean()*100:+9.4f} "
              f"{tilt.mean()*100:+9.4f} {gain:+9.4f}")
    hg = np.array(hist_gain)
    if len(hg):
        print(f"\n  历史阈值口径: 有效 {len(hg)} 季, 改善>0 {int((hg>0).sum())}/{len(hg)}, "
              f"均值 {hg.mean():+.4f}%, 中位 {np.median(hg):+.4f}%, 最差 {hg.min():+.4f}%")
        k, n = int((hg > 0).sum()), len(hg)
        pval = sum(comb(n, i) for i in range(k, n + 1)) / 2 ** n
        print(f"  符号检验 p = {pval:.4f} {'显著' if pval < 0.05 else '不显著'}")

    print("\n=== 3. 对照: 同样口径下我们自己的 slope 占比 ===")
    og = []
    for i in range(2, len(qs)):
        past = h[h.q < qs[i]]
        cur = h[h.q == qs[i]]
        if len(cur) < 60 or len(past) < 200:
            continue
        cut = past.our_share.median()
        tilt = np.where(cur.our_share.to_numpy() < cut, cur.mL.to_numpy(), cur.mS.to_numpy())
        og.append((tilt.mean() - cur.mL.mean()) * 100)
    og = np.array(og)
    if len(og):
        print(f"  slope 占比: 改善>0 {int((og>0).sum())}/{len(og)}, 均值 {og.mean():+.4f}%, "
              f"最差 {og.min():+.4f}%")


if __name__ == "__main__":
    main()
