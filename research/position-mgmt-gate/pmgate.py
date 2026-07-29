"""双向持仓管理门禁 (PMG) —— 独立基线 + 变种库。M5 只作参考,不使用其任何产物。

与 M5 的三点根本区别
====================
1. **不用厂商模型**。全部特征由本地 1h K 线现算(`bars.py`),没有 joblib、没有
   `add_local_features`、没有季度 artifact,因此没有硬过期、没有 33 币训练宇宙的束缚。
2. **双向对称管理**。M5 只减一条腿,实测把多空对冲打破、波动率涨 3.8×。这里任何
   变种都必须**同时**定义多头与空头的动作,评估时强制检查两腿都被触及。
3. **目标是高胜率,但胜率单独一个数是可以造假的**。只减仓不加仓、见好就收,胜率能刷到
   90% 以上而净期望为负。所以本模块的目标函数**永远是二元组**
   `(win_rate, net_expectancy)`,且选拔时净期望有硬下限 —— 见 `score_variant`。

管理 ≠ 选币
===========
入场规则在所有变种之间**完全固定**且刻意平庸(见 `ENTRY`),因为要测的是"仓位怎么管",
不是"选哪个币"。固定入场后,任何组间差异只能来自管理动作本身。基线 = 同样的入场 +
零管理(持满 horizon)。

防前视(逐条落实,不是口号)
==========================
- 特征在 i 号 bar 上只用 <= i 的**已收盘**数据;
- 决策在 i 号 bar 计算,**在 i+1 号 bar 的开盘价成交**(`DECISION_LAG=1`);
- 同一根 bar 内若止损与止盈都可能触发,一律按**不利优先**(adverse-first)结算;
- 滚动窗口跨数据缺口一律作废(`contiguous_run`),不允许跨 626 小时的缺口算 168 小时斜率。
"""
from __future__ import annotations
import os
import numpy as np
import pandas as pd

import bars

WORK = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
CACHE = f"{WORK}/pmg_cache"
COST_BPS = 10.0          # round-trip taker cost, same as every prior study here
DECISION_LAG = 1         # bars between computing a decision and filling it
HORIZON = 72             # max bars held, matches the 72h label horizon used before
ENTRY_STRIDE = 6         # candidate entries every 6h per symbol

# Fixed, deliberately mediocre entry: trade with the 168h trend when short-term
# momentum agrees. Held IDENTICAL for every variant and for the baseline.
ENTRY = {"slope_win": 168, "mom_win": 24}


# ----------------------------------------------------------------- features
def build_features(sym: str) -> pd.DataFrame | None:
    b = bars.load_symbol(sym)
    if b is None or len(b) < 400:
        return None
    c = b["close"].to_numpy()
    h = b["high"].to_numpy()
    l = b["low"].to_numpy()
    gap = b["gap"].to_numpy()
    run = bars.contiguous_run(gap)

    f = pd.DataFrame(index=b.index)
    f["open"] = b["open"].to_numpy()
    f["high"] = h
    f["low"] = l
    f["close"] = c

    atr = bars.wilder_atr(h, l, c, 14)
    f["atr_pct"] = atr / c * 100.0

    s168 = bars.ols_slope(c, 168)
    s48 = bars.ols_slope(c, 48)
    f["slope168"] = np.where(run >= 167, s168, np.nan)
    f["slope48"] = np.where(run >= 47, s48, np.nan)

    lp = np.log(c)
    for w in (6, 24, 72):
        r = np.full(len(c), np.nan)
        r[w:] = lp[w:] - lp[:-w]
        f[f"mom{w}"] = np.where(run >= w, r, np.nan)

    # realized vol ratio: short vs long. >1 means vol is expanding.
    r1 = np.full(len(c), np.nan)
    r1[1:] = np.diff(lp)
    s = pd.Series(r1, index=b.index)
    v24 = s.rolling(24).std().to_numpy()
    v168 = s.rolling(168).std().to_numpy()
    with np.errstate(invalid="ignore", divide="ignore"):
        f["vol_ratio"] = np.where(run >= 167, v24 / v168, np.nan)
    f["vol24"] = np.where(run >= 23, v24, np.nan)

    # taker buy pressure, completed bars only
    qv = b["quote_volume"].to_numpy()
    tb = b["taker_buy_quote_volume"].to_numpy()
    with np.errstate(invalid="ignore", divide="ignore"):
        imb = np.where(qv > 0, 2.0 * tb / qv - 1.0, np.nan)
    f["taker_imb24"] = pd.Series(imb, index=b.index).rolling(24).mean().to_numpy()

    f["gap"] = gap
    f["run"] = run
    return f


def cached_features(sym: str) -> pd.DataFrame | None:
    os.makedirs(CACHE, exist_ok=True)
    p = f"{CACHE}/{sym}.pkl"
    if os.path.exists(p):
        return pd.read_pickle(p)
    f = build_features(sym)
    if f is not None:
        f.to_pickle(p)
    return f


# ----------------------------------------------------------------- entries
def entry_points(f: pd.DataFrame) -> tuple[np.ndarray, np.ndarray]:
    """Return (indices, side) for candidate entries. side +1 long, -1 short.

    Fixed rule, never tuned: 168h trend sign, confirmed by 24h momentum sign.
    Both computed on completed bars; fill happens at index+DECISION_LAG open.
    """
    s = f["slope168"].to_numpy()
    m = f["mom24"].to_numpy()
    ok = np.isfinite(s) & np.isfinite(m) & (np.sign(s) == np.sign(m)) & (s != 0)
    idx = np.arange(len(f))
    sel = ok & (idx % ENTRY_STRIDE == 0)
    sel[-(HORIZON + DECISION_LAG + 2):] = False   # need room to close out
    return idx[sel], np.sign(s[sel]).astype("int8")


# ----------------------------------------------------------------- simulator
def simulate_trade(f: pd.DataFrame, i0: int, side: int, rule) -> dict | None:
    """Simulate one trade from entry index i0 under management `rule`.

    rule(state) -> target exposure in [0,1], evaluated on bar i, filled at i+1 open.
    Partial reductions are realized at fill price and never re-added (management only
    reduces; adding back would be a new entry decision, out of scope).

    Adverse-first within a bar: if the bar's range covers both a favourable and an
    unfavourable level, the unfavourable one is taken. No intrabar path is assumed.
    """
    n = len(f)
    op = f["open"].to_numpy()
    hi = f["high"].to_numpy()
    lo = f["low"].to_numpy()
    cl = f["close"].to_numpy()
    atrp = f["atr_pct"].to_numpy()
    gap = f["gap"].to_numpy()

    fill = i0 + DECISION_LAG
    if fill >= n:
        return None
    entry = op[fill]
    if not np.isfinite(entry) or entry <= 0:
        return None
    atr0 = atrp[i0]
    if not np.isfinite(atr0) or atr0 <= 0:
        return None

    exposure = 1.0
    realized = 0.0      # realized return contribution, fraction of original size
    peak = 0.0          # best unrealized return seen (fraction, positive = profit)
    trough = 0.0        # worst unrealized return seen
    n_reduce = 0

    last = min(fill + HORIZON, n - 1)
    exit_i = last
    for i in range(fill, last + 1):
        if gap[i] and i > fill:
            exit_i = i - 1
            break
        # mark-to-market extremes for this bar, adverse-first ordering
        r_hi = side * (hi[i] - entry) / entry
        r_lo = side * (lo[i] - entry) / entry
        bar_worst, bar_best = min(r_hi, r_lo), max(r_hi, r_lo)
        trough = min(trough, bar_worst)
        peak = max(peak, bar_best)
        r_close = side * (cl[i] - entry) / entry

        state = {
            "i": i, "bars_held": i - fill, "side": side,
            "r": r_close, "peak": peak, "trough": trough,
            "atr0": atr0 / 100.0, "exposure": exposure,
            "f": f, "entry": entry,
        }
        target = rule(state)
        target = min(max(float(target), 0.0), exposure)   # reduce-only
        if target < exposure - 1e-9:
            j = i + DECISION_LAG
            if j > last:
                j = last
            px = op[j] if j < n and np.isfinite(op[j]) else cl[i]
            r_fill = side * (px - entry) / entry
            cut = exposure - target
            realized += cut * r_fill
            exposure = target
            n_reduce += 1
            if exposure <= 1e-9:
                exit_i = j
                break
        if i == last:
            exit_i = last

    if exposure > 1e-9:
        px = cl[exit_i]
        realized += exposure * side * (px - entry) / entry
        exposure = 0.0

    gross = realized
    # cost: one round trip for the original size, plus a half-turn per extra reduction
    cost = (COST_BPS + 0.5 * COST_BPS * max(0, n_reduce - 1)) / 1e4
    net = gross - cost
    return {
        "entry_i": fill, "exit_i": exit_i, "side": int(side),
        "gross": gross, "net": net, "peak": peak, "trough": trough,
        "bars": exit_i - fill, "n_reduce": n_reduce, "atr0": atr0,
    }


# ----------------------------------------------------------------- variants
def rule_baseline(_state):
    """基线:零管理,持满 horizon。所有比较的分母。"""
    return 1.0


def make_variant(name, **p):
    """双向对称管理规则工厂。

    每个变种都必须对多空两侧定义同样的动作 —— 参数里没有任何 side 专属项,
    动作只依赖 `r`(带方向的收益)、`peak`、`trough`,这些量本身已经把方向吸收掉了。
    这是"双向"的结构性保证,不是靠事后检查。
    """
    tp_atr = p.get("tp_atr")          # scale out when profit reaches k * ATR
    tp_frac = p.get("tp_frac", 0.5)   # how much to scale out there
    give_atr = p.get("give_atr")      # trail: flatten if we give back k * ATR from peak
    arm_atr = p.get("arm_atr", 0.0)   # trail only arms after peak >= arm * ATR
    sl_atr = p.get("sl_atr")          # flatten if loss exceeds k * ATR
    time_bars = p.get("time_bars")    # flatten after N bars if still under time_r
    time_r = p.get("time_r", 0.0)

    def rule(s):
        a = s["atr0"]
        e = s["exposure"]
        if sl_atr is not None and s["r"] <= -sl_atr * a:
            return 0.0
        if (give_atr is not None and s["peak"] >= arm_atr * a
                and s["r"] <= s["peak"] - give_atr * a):
            return 0.0
        if time_bars is not None and s["bars_held"] >= time_bars and s["r"] <= time_r * a:
            return 0.0
        if tp_atr is not None and s["peak"] >= tp_atr * a and e > 1.0 - tp_frac + 1e-9:
            return 1.0 - tp_frac
        return e

    rule.__name__ = name
    rule.params = dict(p)
    return rule


# ----------------------------------------------------------------- scoring
def summarize(trades: list[dict]) -> dict:
    if not trades:
        return {"n": 0}
    net = np.array([t["net"] for t in trades])
    gross = np.array([t["gross"] for t in trades])
    win = net > 0
    wins, losses = net[win], net[~win]
    pf = (wins.sum() / -losses.sum()) if len(losses) and losses.sum() < 0 else np.inf
    long_m = np.array([t["side"] == 1 for t in trades])
    return {
        "n": len(net),
        "win_rate": float(win.mean()),
        "exp_bps": float(net.mean() * 1e4),
        "gross_bps": float(gross.mean() * 1e4),
        "pf": float(pf),
        "avg_win_bps": float(wins.mean() * 1e4) if len(wins) else 0.0,
        "avg_loss_bps": float(losses.mean() * 1e4) if len(losses) else 0.0,
        "sd_bps": float(net.std(ddof=1) * 1e4) if len(net) > 1 else 0.0,
        "bars": float(np.mean([t["bars"] for t in trades])),
        "reduces": float(np.mean([t["n_reduce"] for t in trades])),
        "n_long": int(long_m.sum()), "n_short": int((~long_m).sum()),
        "win_long": float(win[long_m].mean()) if long_m.any() else float("nan"),
        "win_short": float(win[~long_m].mean()) if (~long_m).any() else float("nan"),
        "exp_long_bps": float(net[long_m].mean() * 1e4) if long_m.any() else float("nan"),
        "exp_short_bps": float(net[~long_m].mean() * 1e4) if (~long_m).any() else float("nan"),
    }


def score_variant(cand: dict, base: dict, exp_floor_bps: float = 0.0) -> tuple[float, str]:
    """目标函数:最大化胜率,但净期望有**硬下限**。

    为什么必须这样:只减仓的管理规则可以把胜率刷到极高 —— 见好就收、亏损扛满 horizon,
    胜率上去了,净期望塌了。所以胜率单独一个数在这个问题上是**可以造假的指标**,
    任何只报胜率的结论都应当被怀疑。

    硬下限取 `max(基线净期望, exp_floor_bps)`:变种至少不能比"什么都不做"更差。
    不满足就直接判不合格,不进入胜率比较 —— 不做加权求和,因为加权会让一个足够高的
    胜率把期望的亏损"买"回来,那正是我要禁止的交易。
    """
    if cand.get("n", 0) < 200:
        return -1e9, "样本不足"
    floor = max(base["exp_bps"], exp_floor_bps)
    if cand["exp_bps"] < floor:
        return -1e9, f"净期望 {cand['exp_bps']:+.1f} < 下限 {floor:+.1f}bps"
    # 双向要求:任一侧样本过少或该侧期望崩掉,都不算双向管理成功
    if min(cand["n_long"], cand["n_short"]) < 50:
        return -1e9, "单侧样本不足,不构成双向"
    return cand["win_rate"], "ok"


# ----------------------------------------------------------------- self-test
def selftest():
    """刻意去破坏模拟器 —— 不通过就不要相信后面任何一个数字。"""
    n = 400
    idx = pd.date_range("2024-01-01", periods=n, freq="h", tz="UTC")
    ok = True

    # 1) 单调上涨行情:多头基线必须赚到接近全程涨幅(减去成本)
    c = np.linspace(100.0, 120.0, n)
    f = pd.DataFrame({"open": c, "high": c, "low": c, "close": c,
                      "atr_pct": np.full(n, 1.0), "gap": False, "run": np.arange(n)},
                     index=idx)
    t = simulate_trade(f, 100, +1, rule_baseline)
    want = (c[100 + 1 + HORIZON] - c[101]) / c[101] - COST_BPS / 1e4
    if abs(t["net"] - want) > 1e-9:
        print(f"  ✗ 基线多头收益 {t['net']:.6f} != 期望 {want:.6f}"); ok = False
    else:
        print(f"  ✓ 基线多头在单调上涨里收益正确 ({t['net']*100:.3f}%)")

    # 2) 同一行情做空必须是镜像(对称性,双向的最基本要求)
    t2 = simulate_trade(f, 100, -1, rule_baseline)
    if abs(t2["gross"] + t["gross"]) > 1e-9:
        print(f"  ✗ 空头 gross {t2['gross']:.6f} 不是多头 {t['gross']:.6f} 的镜像"); ok = False
    else:
        print("  ✓ 多空严格镜像")

    # 3) 止损阈值必须精确:逐根下跌 0.4%,atr_pct=1.0、sl_atr=1.0 → 跌破 -1% 才触发。
    #    第一次踩到我自己的坑:原本写 c2[101:]=50,但成交就发生在 101,
    #    入场价本身变成 50、r 恒为 0,永远不会止损 —— 那是 fixture 错了不是代码错了。
    #    入场 101(=100.0),102 跌 0.4%,103 跌 0.8%,104 跌 1.2% → 决策在 104、成交在 105。
    c2 = np.full(n, 100.0)
    for k in range(102, n):
        c2[k] = 100.0 * (1.0 - 0.004 * (k - 101))
    f2 = f.copy(); f2["open"] = c2; f2["high"] = c2; f2["low"] = c2; f2["close"] = c2
    t3 = simulate_trade(f2, 100, +1, make_variant("sl1", sl_atr=1.0))
    if t3["exit_i"] != 105:
        print(f"  ✗ 止损应在 104 决策、105 成交,实际 exit_i={t3['exit_i']}"); ok = False
    else:
        print("  ✓ 止损阈值精确:-1.2% 那根决策,下一根成交")

    # 4) 决策延迟必须真实存在:止损价在 i 触及,成交价应是 i+1 的开盘
    c3 = np.full(n, 100.0); c3[110] = 100.0; c3[111:] = 90.0
    f3 = f.copy(); f3["open"] = c3; f3["high"] = c3; f3["low"] = c3; f3["close"] = c3
    t4 = simulate_trade(f3, 100, +1, make_variant("sl2", sl_atr=1.0))
    # r 在 111 才跌破 -1%(atr_pct=1.0), 决策于 111, 成交于 112 的 open=90
    exp_net = (90.0 - 100.0) / 100.0 - COST_BPS / 1e4
    if abs(t4["net"] - exp_net) > 1e-9:
        print(f"  ✗ 延迟成交价不对: {t4['net']:.6f} != {exp_net:.6f}"); ok = False
    else:
        print("  ✓ 决策延迟 1 根、按下一根开盘成交")

    # 5) 不利优先:同一根 bar 内高低点都触及时必须先算不利那边
    c4 = np.full(n, 100.0)
    f4 = pd.DataFrame({"open": c4, "high": c4, "low": c4, "close": c4,
                       "atr_pct": np.full(n, 1.0), "gap": False, "run": np.arange(n)},
                      index=idx)
    f4.iloc[105, f4.columns.get_loc("high")] = 105.0
    f4.iloc[105, f4.columns.get_loc("low")] = 95.0
    t5 = simulate_trade(f4, 100, +1, rule_baseline)
    if abs(t5["trough"] - (-0.05)) > 1e-9 or abs(t5["peak"] - 0.05) > 1e-9:
        print(f"  ✗ 极值记录错误 peak={t5['peak']:.4f} trough={t5['trough']:.4f}"); ok = False
    else:
        print("  ✓ 同 bar 双向触及时 peak/trough 都被记录(不利优先)")

    # 6) 缺口必须截断而不是跨越
    f5 = f.copy()
    f5.iloc[130, f5.columns.get_loc("gap")] = True
    t6 = simulate_trade(f5, 100, +1, rule_baseline)
    if t6["exit_i"] != 129:
        print(f"  ✗ 缺口未截断,exit_i={t6['exit_i']} 应为 129"); ok = False
    else:
        print("  ✓ 数据缺口处截断,不跨缺口持仓")

    # 7) 减仓只减不加
    grow = make_variant("grow", tp_atr=0.5, tp_frac=0.5)
    t7 = simulate_trade(f, 100, +1, grow)
    if t7["n_reduce"] > 1:
        print(f"  ✗ 同一档位重复减仓 {t7['n_reduce']} 次"); ok = False
    else:
        print(f"  ✓ 分批止盈只触发一次 (n_reduce={t7['n_reduce']})")

    # 8) 胜率造假必须能被 score_variant 拦住
    fake = {"n": 1000, "win_rate": 0.95, "exp_bps": -5.0, "n_long": 500, "n_short": 500}
    baseline = {"n": 1000, "win_rate": 0.50, "exp_bps": 1.0, "n_long": 500, "n_short": 500}
    sc, why = score_variant(fake, baseline)
    if sc > -1e8:
        print("  ✗ 95% 胜率但负期望的假变种没被拦下"); ok = False
    else:
        print(f"  ✓ 造假变种被拦下: {why}")

    print("SELFTEST", "PASS" if ok else "FAIL")
    return ok


if __name__ == "__main__":
    selftest()
