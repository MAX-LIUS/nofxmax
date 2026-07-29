# 厂商方向模型评估脚本（2026-07-29）

结论见 `.ai-memory/coin-direction-vendor-eval.md`。这里只放可复现的分析代码。

## 运行前提

- venv：`pandas / numpy / scipy / scikit-learn`，并能 import 交付物包
  `coin_direction_final_claude_delivery_2026-07-29/`（提供 `HurdleModel`、
  `WinsorStandardizer`、`PlattCalibrator`、`add_local_features`、`AtrExitPolicy` 等）。
- 数据：`data.binance.vision` 月度 + 日度 1h 归档（`fapi.binance.com` 本机 451 地理封锁）。
- **内存**：本机 1 核 / ~1.9GB。所有脚本必须放在 `systemd-run` 瞬态服务里跑并设
  `MemoryMax`，否则会把生产 nofx 拖进内存饥饿（2026-07-29 已经发生过一次）：

  ```sh
  systemd-run --unit=cd-x -p MemoryMax=800M -p CPUWeight=15 \
    -p WorkingDirectory=$PWD /path/to/venv/bin/python -u robust.py
  ```

  不要用 `--scope`（随 shell 退出而死）。

## 执行顺序

| 脚本 | 作用 |
|---|---|
| `walkforward.py` | **先跑这个**。自建季度训练驱动（交付物没有）：12 月训练窗 + 72h purge + 测试下一季 + 每季重训。产出 `wf_cache/<SYM>.pkl`（逐币特征缓存）与 `wf_scored/<季度>.pkl`（打分面板） |
| `diag_degen.py` | 自检：对同一批已拟合模型比较 原始分 / 时间尾部校准 / 4 折交叉拟合 / 厂商常数 四种校准。**这一步阻止了我对自己的产物误判失败** |
| `wf_beta.py` | 决定性分解：横截面去均值后逐币方向优势（市场择时被消掉，只剩选币能力）+ 广度统计 |
| `wf_select.py` / `select_test2.py` | 选币口径对照（含"我方否决 + 随机选币"等公平臂）。注意我方 slope168 只能当**符号否决**，其幅度跨币不可比，不能当排序 |
| `market_regime.py` | 转向市场级：按小时聚合全市场 lean |
| `incremental.py` | **关键**：lean 相对朴素读法（24h 跌币占比）是否有增量。双重排序 + 联合回归 |
| `stability.py` | 逐季稳定性；区分"季度自身中位数"（偷看）与"仅用历史阈值"（可用） |
| `optimize.py` | 用法优化：M0~M5 六种用法对比，全部只用历史阈值 |
| `robust.py` | 鲁棒性：倍率 / 阈值 / **滞后（查时间戳泄漏）** / 成本 / 分段 |
| `real_market_gate*.py` `real_m5.py` | 实盘成交验证。`gate1→4` 是迭代过程，**以 `gate4` 与 `real_m5` 为准**（前两版用 USDT 口径，被仓位大小噪声主导，结论是错的） |

### 第二轮（二次开发，2026-07-29 下午）

| 脚本 | 作用 |
|---|---|
| `mktpanel.py` | 产出 `mkt.pkl`：把 86 个特征按小时取横截面均值的**市场级**面板（40056 小时，2022-01 起，未抽稀）。按小时累加 sum/count，绝不同时装载全部币种（1 核 1.9GB） |
| `struct_test.py` | **被证伪的捷径**：厂商 lean 是否 = 市场均值特征的仿射函数。样本外 R² ≤ 0.05、corr 仅 0.23~0.30 → **不是**。因为 `mean_i σ(w·x_i)` 依赖 `w·x_i` 的横截面**分布**而非其均值，且 Winsorizer 每季重拟合中位数。先求均值就把这部分信息丢了 |
| `local_mkt.py` | 用本地特征直接拟合市场级 ridge（季度 walk-forward、72h purge、λ 只在训练窗内层时间切分上选）。结论：**打不平厂商 lean**（9/15 季、+0.0505% 不显著；λ 在 10↔10000 之间跳，说明内层验证找不到稳定信号） |
| `headtohead.py` | 三方同小时对比（厂商 lean / 本地分数 / 朴素 dn24）。**必须做**：前两个脚本的小时集不同（5205 vs 31273），不同口径不可比 |
| `protocol_grid.py` | 协议敏感性 24 组合（lag×阈值×是否去前2季）。**这是本轮最重要的诚实性检查**：点估计 100% 为正且范围窄（+0.0995%~+0.1552%），但只有 16/24 显著 → 效应真实但功效边缘 |
| `symmetric.py` | 二次开发主线：lean 是方向读数还是波动读数？多头腿 +0.271%/SD、空头腿 −0.246%/SD（**反向且都显著**），而两腿之和不显著 → **是方向读数**，且两侧信息量近似对称。据此提出**对称规则**（看空减 LONG、看多减 SHORT） |
| `matched.py` | **决定性对照**：两个基线都是负期望（−0.107%/h、−0.196%/h），所以任何"少交易"都会显示为正收益，`symmetric.py` 的规则对比被这一点污染。环形位移保留自相关/覆盖率/减仓总量，只打乱时点 → 机械收益仅 +0.0085%，净技巧 +0.0776%（占 91%），p=0.0015 |
| `real_symmetric.py` | 对称规则在实盘 756 笔（LONG 436 / SHORT 320）上的验证：+6.474 bps/笔、+60.12 USDT、环形位移 p=0.0352 **显著**；而 M5 只减 LONG 是 +1.644 bps、p=0.2524 不显著 |
| `robust_short.py` | 空头腿 −79.42bps 分离度的稳健性（这是本轮结论的支点，必须单独查）：截尾**更强**（20% 截尾 −85.22）、剔除最好最差各 10 笔后 p=0.0004、两个交易所各自显著、剔除最大名义额 10% 与前 3 品种后仍显著。**唯一弱点**=逐周只有 4 周且 2026-07-06 那周独扛 |

## 三个必须保留的坑

1. **V4 吃收盘时间**（= 开盘 + 1h），V6 吃开盘时间。+1h 只能对 V4 副本加一次。
2. **训练掩码用 `cov >= 0.80`**（推理侧同一门槛），不是"86 列全有限"——
   `WinsorStandardizer` 本来就会中位数填补 NaN。
3. **实盘口径必须按名义金额归一到 bps**。USDT 求和口径下，几笔大额仓位就能主导方差。
4. **基线是负期望时，任何减仓都会显示为正收益**。评价一条"什么时候减仓"的规则，
   必须配环形位移对照（保留自相关+覆盖率+减仓总量，只打乱时点），否则测的是
   "少交易"而不是"择时"。`matched.py` 实测机械收益只占总收益的 9%。
5. **环形位移比块 bootstrap 功效高得多**。同一个效应块 bootstrap 给边缘 CI
   （~430 个有效块），环形位移给 p=0.0015（31225 小时网格上 4000 次位移）。
   两者不矛盾：前者是区间估计，后者是针对"时点是否有信息"的精确检验。
6. **两个脚本的小时集不同就不能比较**。抽稀面板 5205 小时 vs 完整面板 31273 小时，
   直接比数字会得出相反结论（见 `headtohead.py` 的存在理由）。

## 数据文件

`wf_cache/`、`wf_scored/`、`panel.pkl`、`real_fills.csv` 均未入库（体积 + 含实盘成交明细）。
`real_fills.csv` 由只读方式导出，**绝不能对生产库跑 `store.New`**（会 AutoMigrate）：

```sh
sqlite3 -readonly -csv -header /opt/webstack/nofx/data/data.db "
SELECT symbol, side, exchange_type, entry_time, exit_time, realized_pnl, fee, quantity, entry_price
FROM trader_positions WHERE status='CLOSED' AND entry_time >= 1782777600000 ORDER BY entry_time;"
```
