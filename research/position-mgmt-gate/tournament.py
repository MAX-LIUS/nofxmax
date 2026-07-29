"""循环选拔:滚动 walk-forward 里反复挑变种,看优胜者能不能站得住。

为什么是"循环"而不是"跑一次网格挑最好的"
==========================================
一次全样本网格挑冠军,冠军就是过拟合的定义 —— 它在同一份数据上被挑出来又被表扬。
这里每一轮严格分两段:
    选拔段(in-sample)  → 只在这一段按目标函数挑冠军
    验收段(out-sample) → 冠军在这一段的表现才被记录,**不允许回头改选**
窗口再整体前滚。最终看三件事:
  1. 每轮冠军在**验收段**的表现(唯一可外推的估计);
  2. 冠军**是否稳定** —— 每轮换人说明"最优解"只是噪声的名字;
  3. 相对**基线**(零管理、同一批入场、同一份成本)的差。

两个对照组
==========
`shuffle`:把"管理增量"在交易之间随机重排,保留动作的边际分布与频率,只毁掉
  "在正确的时刻做动作"。打不过它,说明赚的是减仓导致的暴露下降而不是择时。
`fixed`  :不做选拔,全程用同一个固定变种。若循环选拔打不过一个固定变种,
  那"选拔"这一步就是多余的,应当直接说出来。
"""
from __future__ import annotations
import os
import numpy as np
import pandas as pd

import pmgate as P
import fastsim as F

WORK = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
RNG = np.random.default_rng(20260729)
MIN_N = 300
MIN_SIDE = 80


def variant_library() -> dict:
    """变种库:双向对称。向量化后算力便宜,网格可以铺开。

    没有任何 side 专属参数 —— 动作只依赖 r/peak/trough,方向已被这些量吸收,
    所以"双向对称"是结构性的,不靠事后检查。
    """
    lib = {"baseline": {}}
    for sl in (0.75, 1.0, 1.5, 2.0, 2.5, 3.0, 4.0):
        lib[f"sl{sl}"] = {"sl_atr": sl}
    for give in (0.25, 0.5, 0.75, 1.0, 1.5, 2.0):
        for arm in (0.25, 0.5, 1.0, 1.5, 2.0, 3.0):
            lib[f"tr{give}a{arm}"] = {"give_atr": give, "arm_atr": arm}
    for tp in (0.5, 1.0, 1.5, 2.0, 3.0, 4.0):
        for fr in (0.25, 0.5, 0.75):
            lib[f"tp{tp}f{fr}"] = {"tp_atr": tp, "tp_frac": fr}
    for tb in (12, 24, 48):
        for tr in (0.0, 0.5):
            lib[f"time{tb}r{tr}"] = {"time_bars": tb, "time_r": tr}
    for sl in (1.5, 2.0, 3.0):
        for give in (0.5, 1.0):
            for tp in (1.0, 2.0):
                for fr in (0.3, 0.5):
                    k = f"c_sl{sl}_g{give}_tp{tp}f{fr}"
                    lib[k] = {"sl_atr": sl, "give_atr": give, "arm_atr": 1.0,
                              "tp_atr": tp, "tp_frac": fr}
    return lib


def load_paths(syms):
    """一次构造全部路径并按入场时间保留,后续每轮只做布尔切片。"""
    out = []
    for s in syms:
        f = P.cached_features(s)
        if f is None:
            continue
        idx, sides = P.entry_points(f)
        if len(idx) == 0:
            continue
        pp = F.build_paths(f, idx, sides)
        if pp is None:
            continue
        # epoch nanoseconds, not datetime64: `f.index.to_numpy()` on a tz-aware index
        # returns an OBJECT array of Timestamps, which silently breaks comparisons.
        pp["ts"] = f.index.view("int64")[pp["idx"]]
        pp["sym"] = s
        out.append(pp)
        del f
    return out


def run_window(paths, lib, t0, t1):
    """在时间窗内对每个变种汇总。返回 {name: arrays}(拼接所有币)。"""
    acc = {k: {"net": [], "gross": [], "sides": [], "n_reduce": [], "bars": []}
           for k in lib}
    for pp in paths:
        m = (pp["ts"] >= t0.value) & (pp["ts"] < t1.value)
        if not m.any():
            continue
        sub = _slice(pp, m)
        for name, params in lib.items():
            d = F.simulate_variant(sub, params)
            if d is None:
                continue
            for k in acc[name]:
                acc[name][k].append(d[k])
    out = {}
    for name, d in acc.items():
        if not d["net"]:
            out[name] = None
            continue
        out[name] = {k: np.concatenate(v) for k, v in d.items()}
    return out


def _slice(pp, m):
    q = {k: pp[k] for k in ("op", "cl", "n")}
    for k in ("idx", "sides", "fill", "entry", "atr0", "end_col"):
        q[k] = pp[k][m]
    for k in ("r_cl", "peak", "trough", "valid"):
        q[k] = pp[k][m]
    q["m"] = int(m.sum())
    return q


def score(cand: dict, base: dict) -> tuple[float, str]:
    """目标:最大化胜率,净期望有硬下限。见 pmgate.score_variant 的论证。"""
    if cand.get("n", 0) < MIN_N:
        return -1e9, "样本不足"
    if cand["exp_bps"] < base["exp_bps"]:
        return -1e9, f"净期望 {cand['exp_bps']:+.1f} < 基线 {base['exp_bps']:+.1f}"
    if min(cand["n_long"], cand["n_short"]) < MIN_SIDE:
        return -1e9, "单侧样本不足"
    return cand["win_rate"], "ok"


def shuffle_control(base_d, cand_d, n_iter=1000):
    if base_d is None or cand_d is None:
        return None
    b, c = base_d["net"], cand_d["net"]
    m = min(len(b), len(c))
    b, c = b[:m], c[:m]
    delta = c - b
    obs = float((c > 0).mean())
    outs = np.array([float((b + RNG.permutation(delta) > 0).mean())
                     for _ in range(n_iter)])
    return {"obs": obs, "shuf": float(outs.mean()), "p": float((outs >= obs).mean())}


def main():
    syms = sorted(f[:-4] for f in os.listdir(P.CACHE) if f.endswith(".pkl"))
    lib = variant_library()
    print(f"变种库 {len(lib)} 个(含基线), 币 {len(syms)} 个")
    paths = load_paths(syms)
    tot = sum(p["m"] for p in paths)
    print(f"候选入场点 {tot} 个, 已构造路径\n")

    edges = pd.date_range("2022-01-01", "2026-07-01", freq="QS", tz="UTC")
    rounds = [(edges[k], edges[k + 2], edges[k + 3]) for k in range(len(edges) - 3)]
    print(f"循环轮数 {len(rounds)}(选拔 2 季 → 验收 1 季, 步进 1 季)\n")

    recs = []
    for (a, b, c) in rounds:
        ins = run_window(paths, lib, a, b)
        base_in = F.summarize_arr(ins["baseline"])
        if base_in.get("n", 0) < MIN_N:
            continue
        ranked = []
        for name in lib:
            if name == "baseline":
                continue
            sc, why = score(F.summarize_arr(ins[name]), base_in)
            ranked.append((sc, name))
        ranked.sort(reverse=True)
        best_sc, best = ranked[0]
        n_pass = sum(1 for s, _ in ranked if s > -1e8)
        if best_sc <= -1e8:
            print(f"{str(b.date())} 验收: 选拔段无合格变种(0/{len(ranked)} 过期望下限)")
            recs.append({"oos": b.date(), "winner": None, "n_pass": 0})
            continue

        oos = run_window(paths, lib, b, c)
        bo = F.summarize_arr(oos["baseline"])
        wo = F.summarize_arr(oos[best])
        ctl = shuffle_control(oos["baseline"], oos[best])
        recs.append({
            "oos": b.date(), "winner": best, "n_pass": n_pass,
            "in_win": best_sc,
            "oos_win": wo.get("win_rate", np.nan), "base_win": bo.get("win_rate", np.nan),
            "oos_exp": wo.get("exp_bps", np.nan), "base_exp": bo.get("exp_bps", np.nan),
            "oos_pf": wo.get("pf", np.nan), "base_pf": bo.get("pf", np.nan),
            "n": wo.get("n", 0),
            "win_long": wo.get("win_long", np.nan), "win_short": wo.get("win_short", np.nan),
            "shuf": ctl["shuf"] if ctl else np.nan, "shuf_p": ctl["p"] if ctl else np.nan,
        })
        print(f"{str(b.date())} 冠军={best:<20} 选拔段合格 {n_pass:>3}/{len(ranked)} | "
              f"验收胜率 {wo['win_rate']:.3f} vs 基线 {bo['win_rate']:.3f} "
              f"({wo['win_rate']-bo['win_rate']:+.3f}) | "
              f"期望 {wo['exp_bps']:+7.1f} vs {bo['exp_bps']:+7.1f}bps | "
              f"打乱 {ctl['shuf']:.3f} p={ctl['p']:.3f} | n={wo['n']}")

    df = pd.DataFrame(recs)
    df.to_csv(f"{WORK}/tournament.csv", index=False)
    ok = df[df.winner.notna()].copy()
    print(f"\n===== 汇总({len(ok)}/{len(df)} 轮有合格冠军) =====")
    if len(ok) == 0:
        print("没有任何一轮产生合格冠军。在'胜率最大化且净期望不低于基线'下,这套变种库是空的。")
        return
    print("冠军出现次数:")
    for nm, cnt in ok.winner.value_counts().items():
        print(f"  {nm:<22}{cnt}")
    dwin = ok.oos_win - ok.base_win
    dexp = ok.oos_exp - ok.base_exp
    print(f"\n验收胜率增益: 均值 {dwin.mean():+.4f} 中位 {dwin.median():+.4f} "
          f"正 {(dwin>0).sum()}/{len(ok)}")
    print(f"验收期望增益: 均值 {dexp.mean():+.2f}bps 中位 {dexp.median():+.2f} "
          f"正 {(dexp>0).sum()}/{len(ok)}")
    print(f"胜率与期望同时为正的轮数: {((dwin>0)&(dexp>0)).sum()}/{len(ok)}")
    print(f"打乱对照无择时证据(p>0.10)的轮数: {(ok.shuf_p>0.10).sum()}/{len(ok)}")
    print(f"双向: 多头胜率均值 {ok.win_long.mean():.3f} 空头 {ok.win_short.mean():.3f}")

    # 固定变种对照:选拔这一步到底有没有增量
    print("\n===== 对照:不做选拔,全程固定一个变种(验收段合并) =====")
    allo = run_window(paths, lib, edges[2], edges[-1])
    bb = F.summarize_arr(allo["baseline"])
    rows = []
    for name in lib:
        s = F.summarize_arr(allo[name])
        if s.get("n", 0) < MIN_N:
            continue
        rows.append((s["win_rate"], s["exp_bps"], s["pf"], name, s))
    rows.sort(reverse=True)
    print(f"  {'变种':<22}{'胜率':>8}{'期望bps':>10}{'PF':>7}{'均盈bps':>10}{'均亏bps':>10}{'减仓':>6}")
    print(f"  {'baseline':<22}{bb['win_rate']:>8.3f}{bb['exp_bps']:>10.1f}"
          f"{bb['pf']:>7.2f}{bb['avg_win_bps']:>10.0f}{bb['avg_loss_bps']:>10.0f}"
          f"{bb['reduces']:>6.2f}")
    for w, e, pf, name, s in rows[:12]:
        mark = "" if e >= bb["exp_bps"] else "  ← 期望不如基线"
        print(f"  {name:<22}{w:>8.3f}{e:>10.1f}{pf:>7.2f}"
              f"{s['avg_win_bps']:>10.0f}{s['avg_loss_bps']:>10.0f}{s['reduces']:>6.2f}{mark}")
    print("\n注:胜率榜前列若普遍'期望不如基线',正是我在 score() 里拦掉的那种造假 ——"
          "见好就收把胜率刷上去、亏损扛满,净期望塌掉。")


if __name__ == "__main__":
    main()
