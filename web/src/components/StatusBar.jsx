import React from 'react'
import { parseTime } from '../api'
import { t, locale } from '../i18n'

// StatusBar 状态历史条: 最近 N 次探测, 绿=成功 红=失败 灰=无数据
export default function StatusBar({ stats }) {
  const B = 48
  const events = (stats && stats.history) || []
  const recent = events.slice(-B)
  return (
    <>
      <div className="hist">
        {Array.from({ length: B - recent.length }).map((_, i) => <i key={'e' + i}></i>)}
        {recent.map((e, i) => <i key={i} className={e.ok ? 'up' : 'down'}
          title={`${parseTime(e.time).toLocaleString(locale())} ${e.ok ? t('sb.ok') : t('sb.fail')}`}></i>)}
      </div>
      <div className="hist-label">
        <span>{t('sb.early')}</span><span>{t('sb.recent', { n: recent.length })}</span><span>{t('sb.now')}</span>
      </div>
    </>
  )
}
