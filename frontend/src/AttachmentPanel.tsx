import { useEffect, useRef, useState } from 'react'
import { api, del, patch } from './api'
import { Button, Card, Empty } from './components'
import type { AttachmentPlacement, ReportAttachment } from './types'

/**
 * Screen captures attached to a report. Each image becomes its own slide in the
 * exported deck, placed before or after the written content in the order shown
 * here, so the list is the slide order.
 */
export default function AttachmentPanel({ reportId, editable, notify }: {
  reportId: number
  editable: boolean
  notify: (message: string, kind?: 'success' | 'error') => void
}) {
  const [items, setItems] = useState<ReportAttachment[]>()
  const [dragging, setDragging] = useState(false)
  const [busy, setBusy] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  const load = () => api<ReportAttachment[]>(`/api/v1/reports/${reportId}/attachments`).then(setItems)
  useEffect(() => { load().catch(() => setItems([])) }, [reportId])

  const upload = async (files: File[]) => {
    const images = files.filter(file => file.type.startsWith('image/'))
    const rejected = files.length - images.length
    if (rejected > 0) notify(`이미지가 아닌 파일 ${rejected}개는 제외했습니다.`, 'error')
    if (!images.length) return
    setBusy(true)
    try {
      const body = new FormData()
      images.forEach(file => body.append('files', file))
      body.append('placement', 'AFTER')
      await api(`/api/v1/reports/${reportId}/attachments`, { method: 'POST', body })
      await load()
      notify(`이미지 ${images.length}개를 첨부했습니다. 내보내기 시 슬라이드로 추가됩니다.`)
    } catch (error) {
      notify(error instanceof Error ? error.message : '이미지를 첨부할 수 없습니다.', 'error')
    } finally { setBusy(false) }
  }

  // The drop event only fires when the preceding drag events are cancelled.
  const onDragOver = (event: React.DragEvent) => {
    if (!editable) return
    event.preventDefault(); event.stopPropagation()
    event.dataTransfer.dropEffect = busy ? 'none' : 'copy'
    if (!dragging) setDragging(true)
  }
  const onDragLeave = (event: React.DragEvent) => {
    if (event.currentTarget.contains(event.relatedTarget as Node | null)) return
    setDragging(false)
  }
  const onDrop = (event: React.DragEvent) => {
    if (!editable) return
    event.preventDefault(); event.stopPropagation(); setDragging(false)
    if (busy) return
    void upload(Array.from(event.dataTransfer.files ?? []))
  }

  // A capture pasted straight from the clipboard is the common case for a
  // screenshot, so Ctrl+V works anywhere in the panel.
  const onPaste = (event: React.ClipboardEvent) => {
    if (!editable || busy) return
    const files = Array.from(event.clipboardData?.files ?? [])
    if (!files.length) return
    event.preventDefault()
    void upload(files)
  }

  const change = async (item: ReportAttachment, values: Partial<ReportAttachment>) => {
    setItems(current => current?.map(entry => entry.id === item.id ? { ...entry, ...values } : entry))
    try { await patch(`/api/v1/reports/${reportId}/attachments/${item.id}`, values) } catch (error) {
      notify(error instanceof Error ? error.message : '이미지 정보를 저장할 수 없습니다.', 'error')
      await load().catch(() => undefined)
    }
  }
  const move = async (item: ReportAttachment, direction: -1 | 1) => {
    const peers = (items ?? []).filter(entry => entry.placement === item.placement)
    const index = peers.findIndex(entry => entry.id === item.id)
    const target = peers[index + direction]
    if (!target) return
    await Promise.all([
      change(item, { sortOrder: target.sortOrder }),
      change(target, { sortOrder: item.sortOrder }),
    ])
    await load().catch(() => undefined)
  }
  const remove = async (item: ReportAttachment) => {
    if (!confirm(`'${item.caption || item.filename}' 이미지를 삭제하시겠습니까?`)) return
    try { await del(`/api/v1/reports/${reportId}/attachments/${item.id}`); await load(); notify('이미지를 삭제했습니다.') } catch (error) {
      notify(error instanceof Error ? error.message : '이미지를 삭제할 수 없습니다.', 'error')
    }
  }

  const groups: { placement: AttachmentPlacement; name: string }[] = [
    { placement: 'BEFORE', name: '본문 앞 슬라이드' },
    { placement: 'AFTER', name: '본문 뒤 슬라이드' },
  ]

  return <Card title="이미지 캡처 슬라이드" action={items?.length ? <span className="muted-chip">{items.length}개 · 슬라이드로 추가됨</span> : undefined}>
    <p className="muted">화면 캡처를 끌어다 놓거나 붙여넣으면 PPTX 내보내기에서 각 이미지가 한 장의 슬라이드가 됩니다. 본문 앞·뒤 위치와 순서를 지정할 수 있습니다.</p>
    {editable && <div
      className={`file-drop capture-drop ${dragging ? 'dragging' : ''}`}
      onDrop={onDrop} onDragOver={onDragOver} onDragEnter={onDragOver} onDragLeave={onDragLeave}
      onPaste={onPaste} tabIndex={0} role="button"
      onClick={() => inputRef.current?.click()}
      onKeyDown={event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); inputRef.current?.click() } }}>
      <input ref={inputRef} type="file" multiple accept="image/png,image/jpeg,image/gif"
        onChange={event => { void upload(Array.from(event.target.files ?? [])); event.target.value = '' }} />
      <strong>{busy ? '업로드 중…' : dragging ? '여기에 놓으면 슬라이드가 추가됩니다' : '이미지를 끌어다 놓거나 클릭해 선택하세요'}</strong>
      <span>PNG · JPEG · GIF · 클립보드에서 Ctrl+V로 붙여넣기도 지원합니다.</span>
    </div>}
    {items === undefined ? null : !items.length ? <Empty>첨부한 이미지가 없습니다.</Empty> : <div className="capture-groups">
      {groups.map(group => {
        const groupItems = items.filter(item => item.placement === group.placement)
        if (!groupItems.length) return null
        return <section key={group.placement}>
          <h4>{group.name} <small>{groupItems.length}장</small></h4>
          <div className="capture-list">{groupItems.map((item, index) => <figure key={item.id}>
            <img src={`/api/v1/reports/${reportId}/attachments/${item.id}`} alt={item.caption || item.filename} loading="lazy" />
            <figcaption>
              {editable ? <input value={item.caption} maxLength={240} placeholder="슬라이드 제목 (비우면 파일명)"
                onChange={event => setItems(current => current?.map(entry => entry.id === item.id ? { ...entry, caption: event.target.value } : entry))}
                onBlur={event => change(item, { caption: event.target.value })} />
                : <strong>{item.caption || item.filename}</strong>}
              <small>{item.width}×{item.height} · {(item.sizeBytes / 1024).toFixed(0)} KB</small>
              {editable && <div className="capture-actions">
                <select value={item.placement} onChange={event => change(item, { placement: event.target.value as AttachmentPlacement })}>
                  <option value="BEFORE">본문 앞</option>
                  <option value="AFTER">본문 뒤</option>
                </select>
                <button disabled={index === 0} onClick={() => move(item, -1)} title="앞으로">↑</button>
                <button disabled={index === groupItems.length - 1} onClick={() => move(item, 1)} title="뒤로">↓</button>
                <button className="remove-button" onClick={() => remove(item)}>삭제</button>
              </div>}
            </figcaption>
          </figure>)}</div>
        </section>
      })}
    </div>}
  </Card>
}
