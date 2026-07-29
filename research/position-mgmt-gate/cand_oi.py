"""候选 3:持仓量 / 多空比 (Binance daily metrics 归档,小时聚合)。

这是三个候选里信息量先验最高的一组:OI 变化 + 大户多空比 + taker 买卖量比,
都是**订单流/持仓结构**读数,单币价格路径里天然没有。

对齐口径:fetch_metrics.py 已用 resample("1h", label="left", closed="left").last()
聚合,即每根小时 bar 只含该小时内最后一个 5min 快照 —— 不含未来。这里再做一次
reindex(ffill) 对齐到 K 线索引,同样只向后填。

切分点:metrics 覆盖 2024-06-01 起,若沿用 2024-07-01 训练期只有 1 个月。
覆盖中点约 2025-06-28,故取 2025-07-01 —— 在看任何结果之前定好。

特征(带 _dir 的乘 side,让多空可比):
  oi_chg24_dir    OI 24h 变化率 × side(持仓在往持仓方向堆积?)
  oi_px_div_dir   OI 变化与价格变化的背离 × side(加仓推涨 vs 空头回补)
  tt_ratio_dir    大户账户数多空比偏离其 30d 均值 × side
  tt_pos_dir      大户持仓量多空比偏离其 30d 均值 × side
  gl_ratio_dir    全市场账户多空比偏离 × side(散户拥挤度)
  taker_dir       taker 主买/主卖量比偏离 1 × side
  oi_z            OI 绝对水平相对 30d 的 z(不带方向,纯拥挤度)
"""
import os, numpy as np, pandas as pd
import pmgate as P, screen as S

MD="/root/.claude/jobs/5cbb3cf4/tmp/cdeval/metrics"
SPLIT_OI="2025-07-01"
W=24*30  # 30 天窗口

syms=sorted(f[:-4] for f in os.listdir(P.CACHE) if f.endswith('.pkl'))
cols={k:{} for k in ["oi_chg24_dir","oi_px_div_dir","tt_ratio_dir","tt_pos_dir",
                     "gl_ratio_dir","taker_dir","oi_z"]}
n_ok=0
for s in syms:
    p=f"{MD}/{s}.pkl"
    if not os.path.exists(p): continue
    d=pd.read_pickle(p).sort_index()
    d=d[~d.index.duplicated(keep="last")]
    oi=pd.to_numeric(d.get("sum_open_interest"),errors="coerce")
    oiv=pd.to_numeric(d.get("sum_open_interest_value"),errors="coerce")
    if oi is None or oi.dropna().empty: continue
    # 用 OI 名义价值/OI 张数反推价格,避免再拉一份 K 线(且两者同源同刻,无错位)
    px=(oiv/oi.replace(0,np.nan))
    oi_chg=oi.pct_change(24)
    px_chg=px.pct_change(24)
    cols["oi_chg24_dir"][s]=oi_chg
    cols["oi_px_div_dir"][s]=oi_chg*np.sign(px_chg.fillna(0))
    for name,key in [("tt_ratio_dir","count_toptrader_long_short_ratio"),
                     ("tt_pos_dir","sum_toptrader_long_short_ratio"),
                     ("gl_ratio_dir","count_long_short_ratio"),
                     ("taker_dir","sum_taker_long_short_vol_ratio")]:
        r=pd.to_numeric(d.get(key),errors="coerce")
        if r is None: cols[name][s]=pd.Series(dtype="float64"); continue
        cols[name][s]=r-r.rolling(W,min_periods=48).mean()
    mu=oi.rolling(W,min_periods=48).mean(); sd=oi.rolling(W,min_periods=48).std()
    cols["oi_z"][s]=(oi-mu)/sd.replace(0,np.nan)
    n_ok+=1
print(f"metrics 可用 {n_ok}/{len(syms)} 币")
df=S.decision_points(extra_cols=cols)
print(f"决策样本 {len(df)}  切分点 {SPLIT_OI}")
S.run("持仓量/多空比 (OI变化, OI-价格背离, 大户多空比, taker量比)", df,
      list(cols.keys()), split=SPLIT_OI)
