"""Decisive control: is the damping benefit DIRECTIONAL SKILL, or just the
mechanical benefit of reducing exposure on a negative-expectancy baseline?

Both baselines are negative (-0.1070%/h for half-long-half-short, -0.1957%/h
for all-long), so ANY exposure cut scores positive. The rule comparison in
symmetric.py is therefore confounded.

Control: circular-shift the signal in time. This preserves the signal's
autocorrelation, its coverage, and the exact amount of exposure reduction, and
destroys only its alignment with the outcome. If the real timing does not beat
shifted timing, there is no directional skill and the gain was de-risking.
"""
from __future__ import annotations
import glob
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
RNG = np.random.default_rng(20260729)
NSHIFT = 4000


def load():
    acc = []
    for p in sorted(glob.glob(f"{BASE}/wf_scored/*.pkl")):
        d = pd.read_pickle(p)
        d.index = pd.to_datetime(d.index, utc=True)
        d = d[np.isfinite(d.raw_up) & np.isfinite(d.net_long) & np.isfinite(d.net_short)]
        g = d.groupby(d.index)
        acc.append(pd.DataFrame({"lean": g["raw_up"].mean(),
                                 "mL": g["net_long"].mean(),
                                 "mS": g["net_short"].mean(),
                                 "nv": g.size()}))
        del d, g
    v = pd.concat(acc).sort_index()
    return v[v.nv >= 10]


def main():
    v = load()
    # regular hourly grid so a circular shift is meaningful
    hrs = ((v.index - v.index[0]) / pd.Timedelta(hours=1)).to_numpy().astype("int64")
    span = int(hrs.max()) + 1
    mL, mS = v.mL.to_numpy(), v.mS.to_numpy()
    bear_row = (v.lean <= v.lean.median()).to_numpy()
    sig = np.zeros(span, dtype=bool)
    sig[hrs[bear_row]] = True
    print(f"面板 {len(v)} 小时, 铺在 {span} 小时网格上, 看空占 {bear_row.mean()*100:.0f}%\n")

    def evaluate(bear):
        out = {}
        base_l = mL.mean()
        out["纯多_减半"] = (np.where(bear, 0.5, 1.0) * mL).mean() - base_l
        base_b = (0.5 * mL + 0.5 * mS).mean()
        out["半多半空_只减LONG"] = (0.5 * np.where(bear, 0.5, 1.0) * mL
                              + 0.5 * mS).mean() - base_b
        out["半多半空_对称"] = (0.5 * np.where(bear, 0.5, 1.0) * mL
                          + 0.5 * np.where(~bear, 0.5, 1.0) * mS).mean() - base_b
        out["半多半空_两侧都减"] = (0.5 * np.where(bear, 0.5, 1.0) * mL
                            + 0.5 * np.where(bear, 0.5, 1.0) * mS).mean() - base_b
        return out

    real = evaluate(bear_row)
    keys = list(real)
    null = {k: np.empty(NSHIFT) for k in keys}
    for i in range(NSHIFT):
        rot = np.roll(sig, RNG.integers(1, span))[hrs]
        e = evaluate(rot)
        for k in keys:
            null[k][i] = e[k]

    print("环形位移零假设 %d 次 —— 位移保留了自相关/覆盖率/减仓总量, 只打乱时点\n" % NSHIFT)
    print(f"{'规则':22s} {'真实':>10s} {'零假设均值':>11s} {'零假设p95':>10s} "
          f"{'p值':>8s} {'净技巧':>10s}")
    for k in keys:
        n = null[k]
        p = float((n >= real[k]).mean())
        skill = real[k] - n.mean()
        star = "显著" if p < 0.05 else ""
        print(f"  {k:20s} {real[k]*100:+9.4f}% {n.mean()*100:+10.4f}% "
              f"{np.percentile(n,95)*100:+9.4f}% {p:8.4f} {skill*100:+9.4f}% {star}")

    print("\n解读: '零假设均值' 就是纯粹'少交易'带来的机械收益(与时点无关);")
    print("      '净技巧' = 真实 - 零假设均值, 才是 lean 的方向性贡献;")
    print("      p 值 = 随机时点里有多少比例达到了真实表现。")


if __name__ == "__main__":
    main()
