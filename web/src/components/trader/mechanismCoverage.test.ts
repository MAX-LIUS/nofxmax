import { describe, it, expect } from 'vitest'
import {
  MECHANISM_META,
  classifyMechanism,
  mechanismLabel,
  categoryOf,
} from './protectionPlan'

// 后端 store/attribution.go 的平仓机制全枚举(Mech* 常量,不含 *Open)。
// 这份清单是"前后端归因必须对齐"的唯一契约:后端新增一个机制、前端漏配,
// 面板就会显示"未知" —— 线上实际发生过(structural_sl 落库 15 条却显示未知系统)。
// 后端新增机制时,这里加一行,三条前端路径(映射函数/标签表/分类)会一起被钉住。
const BACKEND_MECHANISMS = [
  'ladder_tp',
  'ladder_sl',
  'full_tp',
  'full_sl',
  'structural_sl',
  'fallback_maxloss_sl',
  'native_trailing',
  'managed_drawdown',
  'break_even_stop',
  'time_stop',
  'max_hold',
  'trailing_take_profit',
  'breadth_breaker',
  'trend_reversal_flip',
  'ai_close',
  'manual_close',
  'liquidation',
  'emergency_protection_close',
  'sync_external',
  'legacy_unknown',
  'unknown_close',
] as const

describe('归因机制前后端对齐', () => {
  it('每个后端机制都有中文标签,不会落到裸 key', () => {
    const missing = BACKEND_MECHANISMS.filter((m) => !MECHANISM_META[m])
    expect(missing).toEqual([])
  })

  it('每个后端机制的 raw reason 都能被 reasonToMechanism 解出,不落 unknown_close', () => {
    const unresolved = BACKEND_MECHANISMS.filter(
      (m) => m !== 'unknown_close' && classifyMechanism(m) === 'unknown_close'
    )
    expect(unresolved).toEqual([])
  })

  it('结构位止损解析正确且不被 full_sl 抢先', () => {
    expect(classifyMechanism('structural_sl')).toBe('structural_sl')
    expect(classifyMechanism('structural_sl_backstop')).toBe('structural_sl')
    expect(mechanismLabel('zh', 'structural_sl')).toBe('结构位止损')
    expect(categoryOf('structural_sl')).toBe('protection')
    // 反向:确认断言非空 —— 未配置的机制确实会落到裸 key
    expect(mechanismLabel('zh', 'not_a_real_mech')).toBe('not_a_real_mech')
  })

  it('保护类机制归到 protection,不会误落 system', () => {
    const protectionMechs = [
      'ladder_sl',
      'structural_sl',
      'break_even_stop',
      'managed_drawdown',
      'max_hold',
      'breadth_breaker',
    ]
    for (const m of protectionMechs) {
      expect(categoryOf(m)).toBe('protection')
    }
  })
})
