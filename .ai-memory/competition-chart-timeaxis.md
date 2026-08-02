# 竞赛对比图：时间轴 / 缩放 / 持仓浮窗（2026-07-29）

## 1. 用户报告的四个症状与真实根因

症状：「1天3天7天30天全部都没有用，水平坐标时间也不跟着周期变，曲线也只有 7.31
以后的，也不能拖动曲线，也不能缩放。」

四个症状其实是**两处截断 + 一个轴类型选择**造成的：

### 根因 A：后端 `hours=0`（"全部"）返回的是**最近 500 行**，不是全部历史
`getEquityHistoryForTraders` 里 `hours<=0` 走 `Equity().GetLatest(traderID, 500)`。
快照约每 3 分钟一条 → 500 行 ≈ **7.5 天**，而 claude 真实历史是 **44.4 天 / 3284
条**。线上实测（旧代码，pid 641545）：

| hours | 返回行数 | 覆盖区间 |
|---|---|---|
| 24 | 49 | 08-02 01:22 .. 08-03 01:11 |
| 168 | 467 | 07-27 .. 08-03 |
| 720 | 2168 | 07-04 .. 08-03 |
| **0（全部）** | **501** | **07-26 13:42 .. 08-03**（只 7.5 天） |

新增 `EquityStore.GetAllAscending()`；`hours=0` 改为真的取全部。

### 根因 B：前端在**合并四个交易员之后**又切掉尾部 500 个分钟桶
`MAX_DISPLAY_POINTS = 500` + `combinedData.slice(-500)`。四个交易员各自时刻不同,
合并后桶数是单人的数倍,再切最后 500 桶 → **不管选哪个周期都只剩最近约 1.8 天**。
线上实测旧行为（这直接解释了「只有 7.31 以后」）：

| 按钮 | 服务端每人行数 | 合并后桶数 | 旧 last-500 实际覆盖 |
|---|---|---|---|
| 1D | 64/49/49/48 | 113 | 08-02 01:17 .. 08-03 01:11 |
| 3D | 253/191/189/190 | 503 | **07-31** 01:23 .. 08-03 |
| 7D | 625/469/467/467 | 1391 | **07-31** 01:23 .. 08-03 |
| 30D | 2854/2175/2168/2165 | 5398 | **07-31** 01:23 .. 08-03 |
| 全部 | 501×4 | 1378 | **07-31** 01:23 .. 08-03 |

即 3D/7D/30D/全部**四个按钮的可见区间完全相同**,与用户描述一字不差。
改为：客户端不再切尾；样本数由服务端 `max_points` 限定（默认 1500）。

### 根因 C：X 轴是**类别轴 + 预格式化字符串**
`dataKey="time"`,而 `time` 是在合并时按 `selectedHours` 生成的字符串。后果有三：
①样本间距与真实时间无关 → 数据空洞（重启/断线）显示成一个样本宽的台阶,看不出来;
②标签格式取自**按钮**而非屏幕上真实跨度 → 缩放后标签不再描述可见内容;
③类别轴**没有数值域可缩放/平移** → 天然不可能实现拖动与缩放。
改为数值时间轴（`type="number" scale="time" domain={...} allowDataOverflow`）。

## 2. 降采样必须保峰谷,不能用等间隔抽样
这条曲线是用来读回撤的。等间隔抽样会跨过尖峰把它抹掉 → **同一条曲线在不同缩放级别
上会给出不同的最大回撤**。`api/equity_downsample.go` 用分桶取**每桶最小值和最大值**
并按时间顺序输出（每桶≤2 点）,再钉住首尾真实端点。已由测试验证 claude 真实曲线在
300 点预算下峰 **299.61** / 谷 **138.74** 与全量完全一致。

## 3. 浮窗持仓额度：用 `trader_positions` 回放,不新增字段
`trader_equity_snapshots` 有 `position_count`/`margin_used_pct` 但**没有名义额度**。
若新增列则只覆盖未来、整段历史空白,所以改为回放：新增
`PositionStore.GetExposureAtTimes(traderID, timesMs)`,把 entry/exit 做成扫描线事件
（`+notional` / `-notional`）,与请求时刻一起排序后单趟走完。
- 名义额度 = `entry_price * entry_quantity`（与既有 `GetSideExposureSeries` 一致）。
  **不是** mark 价名义额度：历史 mark 价没存,那需要按每个时刻逐 symbol 拉 K 线。
  因此 UI 明确写「开仓价 x 数量」,不冒充实时敞口。
- 同时返回 `position_count`（快照记录,来自交易所）与 `position_count_recon`（本地
  回放）,**不合并成一个数**：两者不一致正是本地账本漂移的唯一信号。
  生产数据实测一致率：claude 91.7%、GPT 98.0%、CR 95.7%、BN 97.6%（最近 400 条
  100%）→ 漂移集中在早期历史。

## 4. 浮窗里的持仓读数必须标注"多久之前的"
四个交易员几乎从不在同一分钟采样。生产实测（07-25 起）分钟桶里：

| 同一分钟内的交易员数 | 桶数 | 占比 |
|---|---|---|
| 1 | 1271 | **69.7%** |
| 2 | 331 | 18.1% |
| 3 | 159 | 8.7% |
| 4 | 63 | 3.5% |

→ 若"持仓字段只在本人有新样本时才显示",**四行里有三行几乎永远是空的**。所以持仓
字段沿用上一次读数,但必须带 `_age_ms` 并在浮窗打 `-Nm` 标签,否则等于把旧读数伪装成
当前时刻的读数。（pnl 沿用是为了线连续；**不向前回填**：晚开始的交易员在更早的行必须
缺席,否则图上会显示它在存在之前就在参赛。）

## 5. 自查发现并修掉的三个自身缺陷（都由测试抓到）
1. **`clampDomain` 的最小跨度形同虚设**：算出了修正后的 span 却只在视图撞到数据边界
   时才写回 → 缩放到数据中部仍可塌缩到一个点。改为无条件写回。
2. **`zoomDomain` 放行 NaN 锚点**：`Math.min/max` 对 NaN 是**传播而非夹取**,NaN 锚
   点产生 NaN 域 → 整图空白。布局未稳定时 `pointerFrac` 确实会返回 NaN,必须显式判定。
3. **`getEquityHistoryForTraders` 对 `traderManager==nil` 段错误**：这是个**公开免鉴权**
   端点,为了一个可选的实时尾点而解引用空指针不值当,已加 nil 分支。

## 6. 复现/验证要点（下次改这块必读）
- 构造 fixture 时**不要**用 `sqlite3 .mode insert`（按位置插值）：`AutoMigrate` 生成的
  列序与生产库不同（`entry_decision_cycle` 在第 11 列 vs 生产库靠后）,会把
  `entry_price` 灌进 `entry_time`、`'sync'` 灌进 `status`,表面看是"回放全 0"的假 bug。
  必须**按列名**拷贝。
- 组件级渲染测试在 jsdom 下需要 stub `ResizeObserver`（recharts `ResponsiveContainer`
  会构造它）并 mock `getBoundingClientRect`,否则容器高宽为 0、什么都不渲染。
- 测试 fixture 的采样间隔要贴合真实（生产约每 20 分钟一条,3200 条≈44 天）。首版按
  3 分钟造 3200 条只有 6.7 天,断言"跨 45 天"直接失败。
- 生产库只能 `sqlite3 -readonly`；**不要**对生产库跑 `store.New`（会 AutoMigrate）。

## 7. 改动清单
后端：`store/equity.go`(`GetAllAscending`)、`store/position.go`(`GetExposureAtTimes`)、
`api/equity_downsample.go`(新)、`api/handler_competition.go`（`hours=0` 语义、
`max_points`、持仓字段、`sampling` 元数据、nil 守卫）。
前端：`web/src/components/charts/timeAxis.ts`(新)、`comparisonMerge.ts`(新)、
`ComparisonChart.tsx`（数值时间轴/滚轮缩放/拖动平移/双击复位/按钮组/可见区间读数/
浮窗持仓）、`lib/api/data.ts`(`max_points`)、`i18n/translations.ts`（三语言）。
测试：`api/equity_downsample_test.go`(6)、`api/equity_batch_integration_test.go`(4,
需 `NOFX_EQUITY_FIXTURE`)、`store/exposure_at_times_test.go`(7)、
`timeAxis.test.ts`(18)、`comparisonMerge.test.ts`(9)、`ComparisonChart.test.tsx`(7)。
