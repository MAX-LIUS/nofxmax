"""向量化模拟器。逐笔 Python 循环要 3.6 小时,这里做到分钟级。

**它必须与 `pmgate.simulate_trade` 逐笔一致**,否则速度毫无意义 ——
`crosscheck()` 就是干这个的,不通过不许用。

能向量化的原因:本变种族的每个动作都是"路径上第一个满足条件的 bar",
而条件只依赖 r / peak / trough,这些都是累积极值,可以整块算:
    flatten_idx = min(止损触发, 移动止盈触发, 时间止损触发, horizon 末, 缺口截断)
    partial_idx = peak 首次 >= tp*ATR 的 bar
    若 flatten_idx <= partial_idx 则分批不发生(与标量版的判定顺序一致:
    止损/移动止盈/时间止损都在分批之前 return)
成交仍然延后 1 根、按开盘价。
"""
from __future__ import annotations
import numpy as np
import pandas as pd

import pmgate as P

H = P.HORIZON
LAG = P.DECISION_LAG
COST = P.COST_BPS / 1e4


def build_paths(f: pd.DataFrame, idx: np.ndarray, sides: np.ndarray):
    """构造 (m, H+1) 的收益矩阵与辅助量。列 j = 入场后第 j 根 bar。"""
    op = f["open"].to_numpy()
    hi = f["high"].to_numpy()
    lo = f["low"].to_numpy()
    cl = f["close"].to_numpy()
    atrp = f["atr_pct"].to_numpy()
    gap = f["gap"].to_numpy()
    n = len(op)

    fill = idx + LAG
    ok = (fill < n) & np.isfinite(op[np.minimum(fill, n - 1)])
    ok &= np.isfinite(atrp[idx]) & (atrp[idx] > 0)
    ok &= op[np.minimum(fill, n - 1)] > 0
    idx, sides, fill = idx[ok], sides[ok], fill[ok]
    m = len(fill)
    if m == 0:
        return None

    entry = op[fill]
    atr0 = atrp[idx] / 100.0
    # column offsets, clipped at the series end
    cols = np.arange(H + 1)
    pos = fill[:, None] + cols[None, :]
    last_valid = np.minimum(pos, n - 1)
    beyond = pos > n - 1

    sd = sides[:, None].astype("float64")
    e = entry[:, None]
    r_hi = sd * (hi[last_valid] - e) / e
    r_lo = sd * (lo[last_valid] - e) / e
    r_cl = sd * (cl[last_valid] - e) / e
    bar_best = np.maximum(r_hi, r_lo)
    bar_worst = np.minimum(r_hi, r_lo)

    # gap truncation: first column >0 where gap is True ends the trade at col-1
    g = gap[last_valid].copy()
    g[:, 0] = False
    g |= beyond
    gap_first = np.where(g.any(axis=1), g.argmax(axis=1), H + 1)
    end_col = np.minimum(gap_first - 1, H)
    end_col = np.maximum(end_col, 0)

    valid = cols[None, :] <= end_col[:, None]
    peak = np.maximum.accumulate(np.where(valid, bar_best, -np.inf), axis=1)
    trough = np.minimum.accumulate(np.where(valid, bar_worst, np.inf), axis=1)
    return dict(idx=idx, sides=sides, fill=fill, entry=entry, atr0=atr0,
                r_cl=r_cl, peak=peak, trough=trough, valid=valid,
                end_col=end_col, op=op, cl=cl, n=n, m=m)


def _first_true(cond, valid, end_col):
    c = cond & valid
    has = c.any(axis=1)
    first = np.where(has, c.argmax(axis=1), end_col + 1)
    return first


def simulate_variant(pp, params) -> dict | None:
    """对一批已构造好的路径跑一个变种。返回逐笔数组。"""
    if pp is None:
        return None
    valid, end_col = pp["valid"], pp["end_col"]
    a = pp["atr0"][:, None]
    r_cl, peak = pp["r_cl"], pp["peak"]
    m = pp["m"]
    BIG = end_col + 1

    trig = np.full(m, 0, dtype="int64") + BIG
    sl = params.get("sl_atr")
    if sl is not None:
        trig = np.minimum(trig, _first_true(r_cl <= -sl * a, valid, end_col))
    give = params.get("give_atr")
    if give is not None:
        arm = params.get("arm_atr", 0.0)
        cond = (peak >= arm * a) & (r_cl <= peak - give * a)
        trig = np.minimum(trig, _first_true(cond, valid, end_col))
    tb = params.get("time_bars")
    if tb is not None:
        tr = params.get("time_r", 0.0)
        cols = np.arange(peak.shape[1])[None, :]
        cond = (cols >= tb) & (r_cl <= tr * a)
        trig = np.minimum(trig, _first_true(cond, valid, end_col))

    flat_dec = np.minimum(trig, end_col)          # bar on which we decide to flatten
    flattened = trig <= end_col

    tp = params.get("tp_atr")
    frac = params.get("tp_frac", 0.5)
    if tp is not None:
        p_dec = _first_true(peak >= tp * a, valid, end_col)
        # 标量版顺序:止损/移动/时间在分批之前 return,所以同一根或更早触发时分批不发生
        part = p_dec <= np.minimum(flat_dec, end_col)
        part &= p_dec < BIG
        part &= ~(flattened & (trig <= p_dec))
    else:
        p_dec = BIG.copy()
        part = np.zeros(m, dtype=bool)
        frac = 0.0

    op, cl, n = pp["op"], pp["cl"], pp["n"]
    fill, entry, sides = pp["fill"], pp["entry"], pp["sides"]

    def px_at(dec_col):
        """成交价:决策 col 的下一根开盘;越界或不可用退化为该 col 收盘。

        **成交列必须夹在 end_col 以内** —— 标量版里是 `if j > last: j = last`。
        漏了这一步会让"在最后一根 bar 上触发"的交易成交到路径之外,
        逐笔对齐时正是这 7 笔出现偏差(tr1.5 / tp3.0 这些触发很晚的变种)。
        """
        jcol = np.minimum(dec_col + LAG, end_col)
        j = fill + jcol
        jc = np.minimum(j, n - 1)
        p = op[jc]
        bad = (j > n - 1) | ~np.isfinite(p) | (p <= 0)
        fallback = cl[np.minimum(fill + dec_col, n - 1)]
        return np.where(bad, fallback, p)

    # 平掉剩余的那一笔:若触发了就用触发决策价,否则用 end_col 的收盘
    r_exit_trig = sides * (px_at(flat_dec) - entry) / entry
    r_exit_end = sides * (cl[np.minimum(fill + end_col, n - 1)] - entry) / entry
    r_exit = np.where(flattened, r_exit_trig, r_exit_end)

    r_part = sides * (px_at(p_dec) - entry) / entry
    f_part = np.where(part, frac, 0.0)
    gross = f_part * r_part + (1.0 - f_part) * r_exit

    n_reduce = part.astype("int64") + flattened.astype("int64")
    cost = (COST + 0.5 * COST * np.maximum(0, n_reduce - 1))
    net = gross - cost
    return {"net": net, "gross": gross, "sides": sides,
            "n_reduce": n_reduce, "bars": np.where(flattened, flat_dec + LAG, end_col)}


def summarize_arr(d) -> dict:
    if d is None or len(d["net"]) == 0:
        return {"n": 0}
    net = d["net"]
    win = net > 0
    losses = net[~win]
    wins = net[win]
    pf = (wins.sum() / -losses.sum()) if len(losses) and losses.sum() < 0 else np.inf
    lm = d["sides"] == 1
    return {
        "n": int(len(net)), "win_rate": float(win.mean()),
        "exp_bps": float(net.mean() * 1e4), "gross_bps": float(d["gross"].mean() * 1e4),
        "pf": float(pf), "sd_bps": float(net.std(ddof=1) * 1e4) if len(net) > 1 else 0.0,
        "avg_win_bps": float(wins.mean() * 1e4) if len(wins) else 0.0,
        "avg_loss_bps": float(losses.mean() * 1e4) if len(losses) else 0.0,
        "bars": float(d["bars"].mean()), "reduces": float(d["n_reduce"].mean()),
        "n_long": int(lm.sum()), "n_short": int((~lm).sum()),
        "win_long": float(win[lm].mean()) if lm.any() else float("nan"),
        "win_short": float(win[~lm].mean()) if (~lm).any() else float("nan"),
        "exp_long_bps": float(net[lm].mean() * 1e4) if lm.any() else float("nan"),
        "exp_short_bps": float(net[~lm].mean() * 1e4) if (~lm).any() else float("nan"),
    }


def crosscheck(sym="BTCUSDT", limit=400):
    """逐笔对齐标量版。不通过就不要用这个模拟器。"""
    import tournament as T
    f = P.cached_features(sym)
    idx, sides = P.entry_points(f)
    idx, sides = idx[:limit], sides[:limit]
    pp = build_paths(f, idx, sides)
    lib = T.variant_library()
    worst = 0.0
    bad = 0
    for name, rule in lib.items():
        params = getattr(rule, "params", {})
        fastd = simulate_variant(pp, params)
        slow = []
        for i0, sd in zip(pp["idx"], pp["sides"]):
            t = P.simulate_trade(f, int(i0), int(sd), rule)
            slow.append(np.nan if t is None else t["net"])
        slow = np.array(slow)
        d = np.abs(fastd["net"] - slow)
        mx = float(np.nanmax(d))
        nb = int((d > 1e-9).sum())
        worst = max(worst, mx)
        bad += nb
        flag = "" if nb == 0 else f"  <== {nb} 笔不一致"
        print(f"  {name:<20} maxdiff={mx:.2e}{flag}")
    print(f"\n最大逐笔偏差 {worst:.2e}, 不一致笔数 {bad}")
    print("CROSSCHECK", "PASS" if bad == 0 else "FAIL")
    return bad == 0


if __name__ == "__main__":
    crosscheck()
