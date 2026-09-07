import { useState } from 'react'
import { ChevronDown, Download, FileText, ImageOff, Paperclip, Trash2 } from 'lucide-react'

import { api } from '../api'
import { displayName, formatAddressList, formatBytes, formatExactWhen, initials } from '../format'
import type { Message } from '../types'

interface Props {
  message: Message
  expanded: boolean
  onToggle: () => void
  onDelete: (id: string) => void
}

export function MessageCard({ message, expanded, onToggle, onDelete }: Props) {
  const [showRemoteImages, setShowRemoteImages] = useState(false)
  const [tab, setTab] = useState<'html' | 'text'>(message.html_body ? 'html' : 'text')

  const sender = message.from?.[0]
  // Inline parts belong to the body; listing them again as downloads is noise.
  const files = message.attachments?.filter((a) => !a.inline) ?? []

  return (
    <article className="rounded-lg border border-[var(--color-line)] bg-[var(--color-surface)]">
      <header
        className="flex cursor-pointer items-start gap-3 p-4"
        onClick={onToggle}
        role="button"
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            onToggle()
          }
        }}
      >
        <span
          aria-hidden
          className="grid size-9 shrink-0 place-items-center rounded-full bg-[var(--color-accent)]/15 text-xs font-semibold text-[var(--color-accent)]"
        >
          {initials(sender)}
        </span>

        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            <span className="truncate text-sm font-medium">{displayName(sender)}</span>
            {message.direction === 'outbound' && (
              <span className="rounded border border-[var(--color-line)] px-1.5 text-[11px] text-[var(--color-ink-muted)]">
                sent
              </span>
            )}
            <span className="ml-auto shrink-0 text-xs text-[var(--color-ink-muted)]">
              {formatExactWhen(message.sent_at || message.created_at)}
            </span>
          </div>

          <p className="truncate text-xs text-[var(--color-ink-muted)]">
            to {formatAddressList(message.to) || '(nobody)'}
          </p>

          {expanded && (
            <dl className="mt-2 space-y-0.5 text-xs text-[var(--color-ink-muted)]">
              {message.cc?.length ? <Row label="Cc" value={formatAddressList(message.cc)} /> : null}
              {/* Bcc is reconstructed from the envelope; it appears in no
                  header, and seeing it is often the reason to open a message. */}
              {message.bcc?.length ? <Row label="Bcc" value={formatAddressList(message.bcc)} /> : null}
              {message.reply_to?.length ? (
                <Row label="Reply-To" value={formatAddressList(message.reply_to)} />
              ) : null}
            </dl>
          )}
        </div>

        <ChevronDown
          className={`size-4 shrink-0 text-[var(--color-ink-muted)] transition-transform ${
            expanded ? 'rotate-180' : ''
          }`}
          aria-hidden
        />
      </header>

      {expanded && (
        <div className="border-t border-[var(--color-line)]">
          <div className="flex flex-wrap items-center gap-1 px-4 py-2 text-xs">
            {message.html_body && (
              <Tab active={tab === 'html'} onClick={() => setTab('html')}>
                HTML
              </Tab>
            )}
            <Tab active={tab === 'text'} onClick={() => setTab('text')}>
              Text
            </Tab>

            <div className="ml-auto flex items-center gap-1">
              {tab === 'html' && (
                <button
                  type="button"
                  onClick={() => setShowRemoteImages((v) => !v)}
                  title="Remote images are blocked by default, so opening a message cannot tell its sender you read it"
                  className="flex items-center gap-1 rounded px-2 py-1 text-[var(--color-ink-muted)] hover:bg-[var(--color-surface-muted)]"
                >
                  <ImageOff className="size-3.5" aria-hidden />
                  {showRemoteImages ? 'Blocking images' : 'Load remote images'}
                </button>
              )}
              <a
                href={api.rawUrl(message.id)}
                target="_blank"
                rel="noreferrer"
                className="flex items-center gap-1 rounded px-2 py-1 text-[var(--color-ink-muted)] hover:bg-[var(--color-surface-muted)]"
              >
                <FileText className="size-3.5" aria-hidden />
                Source
              </a>
              <button
                type="button"
                onClick={() => onDelete(message.id)}
                className="flex items-center gap-1 rounded px-2 py-1 text-[var(--color-ink-muted)] hover:bg-red-500/10 hover:text-red-500"
              >
                <Trash2 className="size-3.5" aria-hidden />
                Delete
              </button>
            </div>
          </div>

          {tab === 'html' && message.html_body ? (
            <iframe
              // No allow-scripts: the body is untrusted, and the server's
              // Content-Security-Policy forbids scripts too. Either alone
              // would do; both means one mistake is not a hole.
              sandbox="allow-same-origin allow-popups"
              src={api.htmlUrl(message.id, showRemoteImages)}
              title={`Message ${message.id}`}
              className="h-[28rem] w-full border-0 bg-white dark:bg-[var(--color-surface)]"
            />
          ) : (
            <pre className="max-h-[28rem] overflow-auto px-4 pb-4 font-mono text-[13px] leading-relaxed whitespace-pre-wrap">
              {message.text_body || '(this message has no text body)'}
            </pre>
          )}

          {files.length > 0 && (
            <div className="flex flex-wrap gap-2 border-t border-[var(--color-line)] px-4 py-3">
              {files.map((file) => (
                <a
                  key={file.id}
                  href={api.attachmentUrl(file.id)}
                  className="flex items-center gap-2 rounded border border-[var(--color-line)] px-2.5 py-1.5 text-xs hover:bg-[var(--color-surface-muted)]"
                >
                  <Paperclip className="size-3.5 text-[var(--color-ink-muted)]" aria-hidden />
                  <span className="max-w-48 truncate">{file.filename}</span>
                  <span className="text-[var(--color-ink-muted)]">{formatBytes(file.size_bytes)}</span>
                  <Download className="size-3.5 text-[var(--color-ink-muted)]" aria-hidden />
                </a>
              ))}
            </div>
          )}
        </div>
      )}
    </article>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-2">
      <dt className="w-16 shrink-0">{label}</dt>
      <dd className="min-w-0 flex-1 truncate">{value}</dd>
    </div>
  )
}

function Tab({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`rounded px-2 py-1 ${
        active
          ? 'bg-[var(--color-surface-muted)] font-medium'
          : 'text-[var(--color-ink-muted)] hover:bg-[var(--color-surface-muted)]'
      }`}
    >
      {children}
    </button>
  )
}
