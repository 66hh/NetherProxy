import React from 'react'

export default function Modal({ title, onClose, onSave, children }) {
  return (
    <div className="modal-mask" onClick={e => e.target === e.currentTarget && onClose()}>
      <div className="modal">
        <h2>{title}</h2>
        {children}
        <div className="ops">
          <button className="btn" onClick={onClose}>取消</button>
          <button className="btn primary" onClick={onSave}>保存</button>
        </div>
      </div>
    </div>
  )
}

export function Switch({ value, onChange, label }) {
  return (
    <label className="fld"><span>{label}</span>
      <label className="switch">
        <input type="checkbox" checked={!!value} onChange={e => onChange(e.target.checked)} /><i></i>
      </label>
    </label>
  )
}

export function Field({ label, children }) {
  return <label className="fld"><span>{label}</span>{children}</label>
}
