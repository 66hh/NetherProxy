import React from 'react'

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
          title={`${new Date(e.time).toLocaleString()} ${e.ok ? '成功' : '失败'}`}></i>)}
      </div>
      <div className="hist-label"><span>早</span><span>最近 {recent.length} 次探测</span><span>现在</span></div>
    </>
  )
}
