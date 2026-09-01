import { useLayoutEffect, useRef } from 'react'
import type { TextareaHTMLAttributes } from 'react'

export const AUTO_RESIZE_TEXTAREA_CLASS = 'auto-resize-textarea'

/** Resize after first clearing the old explicit height, so shrinking works too. */
export function resizeTextarea(textarea: HTMLTextAreaElement): number {
  textarea.style.height = 'auto'
  const computed = getComputedStyle(textarea)
  const borderHeight = computed.boxSizing === 'border-box'
    ? Math.max(0, textarea.offsetHeight - textarea.clientHeight)
    : 0
  const height = Math.ceil(textarea.scrollHeight + borderHeight)
  textarea.style.height = `${height}px`
  return height
}

export default function AutoResizeTextarea({ className, onInput, value, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  // Covers server-loaded values and programmatic changes such as carrying last
  // week's plan into this week's report. Layout effect prevents a one-frame
  // scrollbar flash before the browser paints the controlled value.
  useLayoutEffect(() => {
    if (textareaRef.current) resizeTextarea(textareaRef.current)
  }, [value])

  // A narrower mobile layout wraps the same text onto more visual rows. Observe
  // width only: resizing height itself also notifies ResizeObserver and must not
  // start a resize loop.
  useLayoutEffect(() => {
    const textarea = textareaRef.current
    if (!textarea) return
    if (typeof ResizeObserver === 'undefined') {
      const resize = () => resizeTextarea(textarea)
      window.addEventListener('resize', resize)
      return () => window.removeEventListener('resize', resize)
    }
    let width: number | undefined
    let resizeFrame = 0
    const observer = new ResizeObserver(entries => {
      const nextWidth = entries[0]?.contentRect.width ?? textarea.getBoundingClientRect().width
      if (nextWidth === width) return
      width = nextWidth
      window.cancelAnimationFrame(resizeFrame)
      // Changing height inside the observer delivery itself makes the observed
      // element notify again in the same cycle. The next frame keeps that
      // intentional second layout out of ResizeObserver's loop protection.
      resizeFrame = window.requestAnimationFrame(() => resizeTextarea(textarea))
    })
    observer.observe(textarea)
    return () => {
      observer.disconnect()
      window.cancelAnimationFrame(resizeFrame)
    }
  }, [])

  const classes = [AUTO_RESIZE_TEXTAREA_CLASS, className].filter(Boolean).join(' ')
  return <textarea {...props} className={classes} value={value} ref={textareaRef}
    onInput={event => {
      resizeTextarea(event.currentTarget)
      onInput?.(event)
    }}/>
}
