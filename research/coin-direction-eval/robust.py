"""Robustness of the deployable arm (M5: damp long size when the lean is bearish,
never reverse direction).

Why M5 and not the higher-mean arms: it has the tightest CI [+0.042,+0.162],
the best worst quarter (-0.106% vs -0.63%), and it cannot flip a trader's
direction, so its failure mode is "sized down in a rally", not "short a rally".

Checks:
  1. Multiplier sweep 0.0~0.8 - is the result an artifact of picking 0.5?
  2. Threshold sweep p30~p70 - is it an artifact of the median?
  3. LAG TEST: use the lean from k hours earlier. If the effect only exists at
     lag 0 and dies at lag 1, that is a timestamp-alignment leak, not a signal.
     This matters because V4 consumes bar-CLOSE time (open+1h).
  4. Cost sweep - does it survive 20bps and 30bps round trips?
  5. Subperiod: first half vs second half.
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


def block_ci(t, v, n_boot=2000, block_days=7):
    t = np.asarray(t, dtype="datetime64[ns]")
    blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // block_days)
    uniq = np.unique(blk)
    by = {b: v[blk == b] for b in uniq}
    s = np.empty(n_boot)
    for i in range(n_boot):
        s[i] = np.concatenate([by[b] for b in RNG.choice(uniq, len(uniq), replace=True)]).mean()
    return (float(np.mean(v)), *np.percentile(s, [2.5, 97.5]))


def build():
    rows = []
    for p in sorted(glob.glob(f"{BASE}/wf_scored/*.pkl")):
        d = pd.read_pickle(p)
        d.index = pd.to_datetime(d.index, utc=True)
        d = d[np.isfinite(d.raw_up) & np.isfinite(d.net_long) & np.isfinite(d.net_short)]
        if d.empty:
            continue
        g = d.groupby(d.index)
        h = pd.DataFrame({"lean": g["raw_up"].mean(),
                          "gL": g["gross_long"].mean() if "gross_long" in d.columns else g["net_long"].mean(),
                          "mL": g["net_long"].mean(), "mS": g["net_short"].mean(),
                          "n": g.size()})
        rows.append(h[h.n >= 10])
        del d, g, h
    h = pd.concat(rows).sort_index()
    h["q"] = pd.PeriodIndex(h.index.tz_localize(None), freq="Q")
    return h


def run(h, mult=0.5, qcut=0.5, lag=0, extra_cost=0.0):
    """Return (per-quarter gains, pooled hourly gain array, times)."""
    qs = sorted(h.q.unique())
    pq, ph, pt = [], [], []
    for i in range(2, len(qs)):
        past, cur = h[h.q < qs[i]], h[h.q == qs[i]]
        if len(cur) < 60 or len(past) < 200:
            continue
        lean = cur.lean.shift(lag) if lag else cur.lean
        ok = lean.notna().to_numpy()
        if ok.sum() < 50:
            continue
        mL = cur.mL.to_numpy()[ok] - extra_cost
        cut = past.lean.quantile(qcut)
        bear = (lean.to_numpy()[ok] <= cut)
        after = np.where(bear, mult * mL, mL)
        pq.append(after.mean() - mL.mean())
        ph.append(after - mL)
        pt.append(cur.index[ok].tz_localize(None).to_numpy("datetime64[ns]"))
    return np.array(pq), np.concatenate(ph), np.concatenate(pt)


def report(tag, h, **kw):
    pq, ph, pt = run(h, **kw)
    k, n = int((pq > 0).sum()), len(pq)
    m, lo, hi = block_ci(pt, ph)
    star = "显著" if lo > 0 else ""
    print(f"  {tag:26s} 季胜 {k:2d}/{n:<2d} 季均 {pq.mean()*100:+7.4f}% "
          f"最差 {pq.min()*100:+7.4f}% 符号p {sign_p(k,n):.4f} "
          f"小时 {m*100:+7.4f}% [{lo*100:+.4f},{hi*100:+.4f}] {star}")
    return lo > 0


def main():
    h = build()
    print(f"面板 {len(h)} 小时 {h.index.min()} ~ {h.index.max()}\n")

    print("=== 1. 减仓倍率扫描 (阈值=历史中位数) ===")
    for m in (0.0, 0.2, 0.3, 0.5, 0.7, 0.8):
        report(f"倍率 {m:.1f}", h, mult=m)

    print("\n=== 2. 阈值分位扫描 (倍率=0.5) ===")
    for q in (0.30, 0.40, 0.50, 0.60, 0.70):
        report(f"阈值 p{int(q*100)}", h, qcut=q)

    print("\n=== 3. 滞后检验 (倍率0.5, 中位数) — 关键: 若只在 lag0 有效即为对齐泄漏 ===")
    for lg in (0, 1, 2, 3, 6, 12):
        report(f"lag {lg}h", h, lag=lg)

    print("\n=== 4. 成本敏感性 (额外往返成本加到多头收益上) ===")
    for c in (0.0, 0.0010, 0.0020):
        report(f"额外 {int(c*10000)}bps", h, extra_cost=c)

    print("\n=== 5. 分段稳定性 (倍率0.5, 中位数) ===")
    qs = sorted(h.q.unique())
    mid = qs[len(qs) // 2]
    for tag, sub in (("前半段", h[h.q < mid]), ("后半段", h[h.q >= mid])):
        pq, ph, pt = run(sub, mult=0.5)
        if len(pq) < 2:
            print(f"  {tag}: 季度不足")
            continue
        m, lo, hi = block_ci(pt, ph)
        print(f"  {tag:26s} 季胜 {int((pq>0).sum())}/{len(pq)} 季均 {pq.mean()*100:+7.4f}% "
              f"小时 {m*100:+7.4f}% [{lo*100:+.4f},{hi*100:+.4f}] "
              f"{'显著' if lo>0 else ''}")

    print("\n=== 6. 保守推荐参数的最终读数 ===")
    ok = report("倍率0.5 / p50 / lag1h", h, mult=0.5, qcut=0.5, lag=1)
    print(f"  -> 采用 lag=1h 作为生产参数(留出计算与下单延迟), 结论: "
          f"{'可上线 SHADOW' if ok else '不达标'}")


if __name__ == "__main__":
    main()
