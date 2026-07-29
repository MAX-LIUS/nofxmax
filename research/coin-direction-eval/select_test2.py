"""Selection test v2 — fair comparators.

v1 ranked coins by |slope168|, which is scale-dependent across coins and is NOT
how our gate is used (we use its SIGN as a veto, never as a ranking). So v1's
negative number for "our gate" is an artifact of a comparison we never make.

Comparators here, all under the same frozen ATR exit + 10bps, top-K per hour:
  A  V4 state           rank by |state| (their signal, side = sign(state))
  B  our veto + random  side = sign(-slope168) veto -> pick K at random among
                        the coins our gate permits (this is our real behaviour:
                        a veto, with no opinion on which coin is better)
  C  our veto + V4 rank rank by |V4 state| but only among coins our veto allows
  D  V4 rank, side=our  V4 picks WHICH coin, our slope picks WHICH side
  E  random K, side=V4  random coin, V4 side -> isolates "side" from "which coin"
Random arms are averaged over 20 draws with a fixed seed.
"""
from __future__ import annotations
import numpy as np
import pandas as pd

P = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval/panel.pkl"
RNG = np.random.default_rng(20260729)
N_DRAW = 20


def block_ci(t, v, n_boot=2000, block_days=14):
    if len(v) == 0:
        return (np.nan, np.nan, np.nan)
    t = np.asarray(t, dtype="datetime64[ns]")
    blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // block_days)
    uniq = np.unique(blk)
    if len(uniq) < 3:
        return (float(np.mean(v)), np.nan, np.nan)
    by = {b: v[blk == b] for b in uniq}
    m = np.empty(n_boot)
    for i in range(n_boot):
        pick = RNG.choice(uniq, size=len(uniq), replace=True)
        m[i] = np.concatenate([by[b] for b in pick]).mean()
    return (float(np.mean(v)), *np.percentile(m, [2.5, 97.5]))


def pct(x):
    return "  n/a   " if not np.isfinite(x) else f"{x*100:+.4f}%"


def run(sub, K, mode):
    """mode: dict(rank=..., side=..., veto=bool)"""
    ts_list, val_list = [], []
    for ts, g in sub.groupby("dt64", sort=True):
        # veto = our gate only permits the side aligned with the 168h slope, so
        # a V4 pick survives only when the two agree (a real intersection).
        if mode.get("veto"):
            gg = g[(g.sl_side != 0) & ((mode["side"] != "v4_side") | (g.v4_side == g.sl_side))]
        else:
            gg = g
        side = gg[mode["side"]].to_numpy()
        gg = gg[side != 0]
        side = side[side != 0]
        if len(gg) == 0:
            continue
        rk = mode["rank"]
        if rk == "random":
            if len(gg) <= K:
                idx = np.arange(len(gg))
            else:
                idx = RNG.choice(len(gg), size=K, replace=False)
        else:
            idx = np.argsort(-np.abs(gg[rk].to_numpy()))[:K]
        nl = gg.net_long.to_numpy()[idx]
        ns = gg.net_short.to_numpy()[idx]
        net = np.where(side[idx] > 0, nl, ns)
        ts_list.append(ts)
        val_list.append(net.mean())
    return np.array(ts_list, dtype="datetime64[ns]"), np.array(val_list)


def main():
    df = pd.read_pickle(P)
    df["decision_time"] = pd.to_datetime(df["decision_time"], utc=True)
    df = df[df.decision_time.dt.hour % 6 == 0].copy()
    df["dt64"] = df["decision_time"].dt.tz_localize(None).astype("datetime64[ns]")
    df["v4_side"] = np.sign(df["v4_state"]).astype(int)
    df["v4_score"] = df["v4_state"].astype(float)
    # our gate is a veto: trade only the side aligned with the 168h slope sign
    df["sl_side"] = np.sign(df["slope168"]).astype(int)
    df["sl_score"] = df["slope168"].astype(float)

    ext_c = set("""ZILUSDT MKRUSDT ALICEUSDT SXPUSDT ZRXUSDT ANKRUSDT ROSEUSDT HBARUSDT XEMUSDT
IOTAUSDT VETUSDT RVNUSDT INJUSDT STGUSDT GTCUSDT IOSTUSDT ICXUSDT OMGUSDT ZENUSDT STMXUSDT
ONTUSDT LPTUSDT BATUSDT ONEUSDT BLZUSDT COTIUSDT OGNUSDT FLMUSDT BAKEUSDT CELRUSDT""".split())

    arms = [
        ("A  V4 排序 + V4 方向", dict(rank="v4_score", side="v4_side")),
        ("B  我们的否决 + 随机选币", dict(rank="random", side="sl_side", veto=True)),
        ("C  我们的否决 + V4 排序", dict(rank="v4_score", side="v4_side", veto=True)),
        ("D  V4 排序 + 我们的方向", dict(rank="v4_score", side="sl_side")),
        ("E  随机选币 + V4 方向", dict(rank="random", side="v4_side")),
    ]

    for pool, sub in (("全池 51 币", df),
                      ("External C (币种留出)", df[df.symbol.isin(ext_c)])):
        print("=" * 112)
        print(f"### {pool}   {sub.symbol.nunique()} 币  {sub.decision_time.min().date()}~{sub.decision_time.max().date()}")
        print("=" * 112)
        t = sub["dt64"].to_numpy()
        for lbl, v in (("基线 全做多", sub.net_long.to_numpy()), ("基线 全做空", sub.net_short.to_numpy())):
            m, lo, hi = block_ci(t, v)
            print(f"  {lbl:<26} 每笔 {pct(m)}  CI [{pct(lo)},{pct(hi)}]")
        for K in (3, 5):
            print("-" * 112)
            for lbl, mode in arms:
                reps = N_DRAW if mode["rank"] == "random" else 1
                ms = []
                for _ in range(reps):
                    tt, vv = run(sub, K, mode)
                    ms.append(vv)
                if reps > 1:
                    L = min(len(x) for x in ms)
                    vv = np.mean([x[:L] for x in ms], axis=0)
                    tt = tt[:L]
                m, lo, hi = block_ci(tt, vv)
                print(f"  top{K} {lbl:<26} 小时={len(vv):>5}  每笔 {pct(m)}  CI [{pct(lo)},{pct(hi)}]"
                      + ("  (20次随机平均)" if reps > 1 else ""))
        print()


if __name__ == "__main__":
    main()
