"""Second development: the evaluation only ever damped LONG. Our book also
takes SHORT. Three questions the vendor never asked:

  Q1 Does the lean separate SHORT outcomes too (damp SHORT when bullish)?
  Q2 Is the symmetric rule better than the LONG-only rule?
  Q3 Is the lean's usable content really directional, or is it a VOLATILITY /
     regime read that would show up as "both sides do worse"? If mean_long and
     mean_short move together with the lean, it is not directional at all and
     damping both sides is the correct use.

Q3 is the one that decides the shape of the rule, so it is tested explicitly:
decompose edge = mL - mS into its two legs and regress each on the lean.
"""
from __future__ import annotations
import glob
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
RNG = np.random.default_rng(20260729)


def block_ci(t, v, n_boot=1500, block_days=7):
    t = np.asarray(t, dtype="datetime64[ns]")
    blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // block_days)
    u = np.unique(blk)
    if len(u) < 3:
        return float(np.mean(v)), np.nan, np.nan
    by = {b: v[blk == b] for b in u}
    s = np.empty(n_boot)
    for i in range(n_boot):
        s[i] = np.concatenate([by[b] for b in RNG.choice(u, len(u), replace=True)]).mean()
    return float(np.mean(v)), *np.percentile(s, [2.5, 97.5])


def load():
    acc = []
    for p in sorted(glob.glob(f"{BASE}/wf_scored/*.pkl")):
        d = pd.read_pickle(p)
        d.index = pd.to_datetime(d.index, utc=True)
        d = d[np.isfinite(d.raw_up) & np.isfinite(d.net_long) & np.isfinite(d.net_short)]
        g = d.groupby(d.index)
        acc.append(pd.DataFrame({
            "lean": g["raw_up"].mean(),
            "mL": g["net_long"].mean(),
            "mS": g["net_short"].mean(),
            "nv": g.size(),
            "q": p.split("/")[-1][:-4]}))
        del d, g
    v = pd.concat(acc).sort_index()
    v = v[v.nv >= 10]
    v["edge"] = v.mL - v.mS
    return v


def main():
    v = load()
    t = v.index.tz_localize(None).to_numpy("datetime64[ns]")
    print(f"面板 {len(v)} 小时, {v.q.nunique()} 季\n")

    print("=== Q3 先答: lean 是方向读数还是波动/环境读数 ===")
    print("  若 mL 与 mS 同向随 lean 变动 => 不是方向读数, 该减的是两边总敞口")
    Z = np.column_stack([((v.lean - v.lean.mean()) / v.lean.std()).to_numpy(),
                         np.ones(len(v))])
    for nm, y in (("多头腿 mL", v.mL.to_numpy()), ("空头腿 mS", v.mS.to_numpy()),
                  ("edge=mL-mS", v.edge.to_numpy()),
                  ("总和 mL+mS", (v.mL + v.mS).to_numpy())):
        b, *_ = np.linalg.lstsq(Z, y, rcond=None)
        blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // 7)
        u = np.unique(blk)
        ib = {x: np.where(blk == x)[0] for x in u}
        bs = np.empty(1000)
        for i in range(1000):
            pick = np.concatenate([ib[x] for x in RNG.choice(u, len(u), replace=True)])
            bb, *_ = np.linalg.lstsq(Z[pick], y[pick], rcond=None)
            bs[i] = bb[0]
        lo, hi = np.percentile(bs, [2.5, 97.5])
        print(f"  {nm:12s} 对 lean 的斜率 {b[0]*100:+.5f}%/SD "
              f"CI [{lo*100:+.5f},{hi*100:+.5f}] {'显著' if (lo>0)==(hi>0) else ''}")

    cut = v.lean.median()
    bear = (v.lean <= cut).to_numpy()
    print(f"\n  看空组(lean<=中位): 多头腿 {v.mL.to_numpy()[bear].mean()*100:+.4f}% "
          f"空头腿 {v.mS.to_numpy()[bear].mean()*100:+.4f}%")
    print(f"  看多组:             多头腿 {v.mL.to_numpy()[~bear].mean()*100:+.4f}% "
          f"空头腿 {v.mS.to_numpy()[~bear].mean()*100:+.4f}%")

    print("\n=== Q1/Q2 三种规则对比 (倍率 0.5, 阈值全样本中位) ===")
    mL, mS = v.mL.to_numpy(), v.mS.to_numpy()
    # A book that is half long half short by notional; damping applies per side.
    rules = {
        "M5 只减 LONG(现方案)": (np.where(bear, 0.5, 1.0), np.ones(len(v))),
        "只减 SHORT(看多时)": (np.ones(len(v)), np.where(~bear, 0.5, 1.0)),
        "对称: 各减不利那侧": (np.where(bear, 0.5, 1.0), np.where(~bear, 0.5, 1.0)),
        "总敞口: 看空时两侧都减": (np.where(bear, 0.5, 1.0), np.where(bear, 0.5, 1.0)),
    }
    base = 0.5 * mL + 0.5 * mS
    print(f"  基线(半多半空不动): {base.mean()*100:+.4f}%/小时")
    print(f"{'规则':24s} {'改善':>10s} {'95%CI':>24s} {'季胜':>7s} {'最差季':>9s} 显著")
    for nm, (wl, ws) in rules.items():
        got = 0.5 * wl * mL + 0.5 * ws * mS
        eff = got - base
        m, lo, hi = block_ci(t, eff)
        qq = pd.Series(eff, index=v.index).groupby(v.q.to_numpy()).mean()
        print(f"  {nm:22s} {m*100:+9.4f}% [{lo*100:+.4f},{hi*100:+.4f}] "
              f"{int((qq>0).sum()):2d}/{len(qq):<3d} {qq.min()*100:+8.4f}% "
              f"{'是' if (lo>0)==(hi>0) else ''}")

    print("\n=== 纯多头簿(我方实况更接近这个)下的对比 ===")
    print(f"  基线(全多不动): {mL.mean()*100:+.4f}%/小时")
    for nm, w in (("看空减半", np.where(bear, 0.5, 1.0)),
                  ("看空清零", np.where(bear, 0.0, 1.0)),
                  ("看空减至 0.75", np.where(bear, 0.75, 1.0))):
        eff = w * mL - mL
        m, lo, hi = block_ci(t, eff)
        qq = pd.Series(eff, index=v.index).groupby(v.q.to_numpy()).mean()
        print(f"  {nm:14s} {m*100:+9.4f}% [{lo*100:+.4f},{hi*100:+.4f}] "
              f"{int((qq>0).sum()):2d}/{len(qq):<3d} 最差季 {qq.min()*100:+.4f}% "
              f"{'显著' if (lo>0)==(hi>0) else ''}")


if __name__ == "__main__":
    main()
