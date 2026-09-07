import { Paperclip } from 'lucide-react'

import { displayName, formatWhen } from '../format'
import type { ConversationSummary } from '../types'

interface Props {
  conversations: ConversationSummary[]
  selectedId: number | null
  loading: boolean
  query: string
  onSelect: (id: number) => void
}

export function ConversationList({ conversations, selectedId, loading, query, onSelect }: Props) {
  if (!conversations.length) {
    return (
      <div className="flex h-full items-center justify-center p-8 text-center text-sm text-[var(--color-ink-muted)]">
        {loading
          ? 'Loading…'
          : query
            ? `Nothing matches “${query}”.`
            : 'No mail captured yet.'}
      </div>
    )
  }

  return (
    <ul className="divide-y divide-[var(--color-line)]">
      {conversations.map((conversation) => {
        const unread = (conversation.unread_count ?? 0) > 0
        const selected = conversation.id === selectedId

        return (
          <li key={conversation.id}>
            <button
              type="button"
              onClick={() => onSelect(conversation.id)}
              aria-current={selected}
              className={`flex w-full flex-col gap-1 px-4 py-3 text-left transition-colors ${
                selected
                  ? 'bg-[var(--color-accent)]/10'
                  : 'hover:bg-[var(--color-surface-muted)]'
              }`}
            >
              <div className="flex items-baseline gap-2">
                {/* The unread dot is the only always-visible cue, so it holds
                    its column whether or not it is filled. */}
                <span
                  aria-hidden
                  className={`size-2 shrink-0 rounded-full ${
                    unread ? 'bg-[var(--color-accent)]' : 'bg-transparent'
                  }`}
                />
                <span
                  className={`min-w-0 flex-1 truncate text-sm ${
                    unread ? 'font-semibold' : 'font-medium'
                  }`}
                >
                  {displayName(conversation.last_from?.[0])}
                </span>
                <span className="shrink-0 text-xs text-[var(--color-ink-muted)]">
                  {formatWhen(conversation.last_activity_at)}
                </span>
              </div>

              <div className="flex items-center gap-2 pl-4">
                <span className={`min-w-0 flex-1 truncate text-sm ${unread ? 'font-medium' : ''}`}>
                  {conversation.subject || '(no subject)'}
                </span>
                {conversation.has_attachments && (
                  <Paperclip className="size-3.5 shrink-0 text-[var(--color-ink-muted)]" aria-label="Has attachments" />
                )}
                {(conversation.message_count ?? 0) > 1 && (
                  <span className="shrink-0 rounded-full border border-[var(--color-line)] px-1.5 text-[11px] text-[var(--color-ink-muted)]">
                    {conversation.message_count}
                  </span>
                )}
              </div>

              {conversation.preview && (
                <p className="truncate pl-4 text-xs text-[var(--color-ink-muted)]">
                  {conversation.preview}
                </p>
              )}
            </button>
          </li>
        )
      })}
    </ul>
  )
}
