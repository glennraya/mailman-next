import { useCallback, useRef, useState } from 'react'
import { Paperclip } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { displayName, formatWhen } from '@/format'
import type { ConversationSummary } from '@/types'

interface Props {
  conversations: ConversationSummary[]
  selectedId: number | null
  loading: boolean
  query: string
  onSelect: (id: number) => void
}

export function ConversationList({ conversations, selectedId, loading, query, onSelect }: Props) {
  // Roving tabindex: the list is one Tab stop, and the arrow keys move within
  // it. The rows stay real buttons, so Enter and Space keep working without a
  // handler of their own. shadcn has no list primitive that does this, so the
  // rows are hand-built and carry their own focus ring.
  const rows = useRef<(HTMLButtonElement | null)[]>([])
  const [focusIndex, setFocusIndex] = useState(0)

  // Searching can shorten the list under a focus index that has already moved
  // past the end. Clamping here is what keeps the list a reachable Tab stop.
  const activeIndex = Math.min(focusIndex, Math.max(conversations.length - 1, 0))

  const onKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLUListElement>) => {
      const last = conversations.length - 1
      if (last < 0) return

      const moves: Record<string, number> = {
        ArrowDown: activeIndex === last ? 0 : activeIndex + 1,
        ArrowUp: activeIndex === 0 ? last : activeIndex - 1,
        Home: 0,
        End: last,
      }

      const next = moves[event.key]
      if (next === undefined) return

      event.preventDefault()
      rows.current[next]?.focus()
    },
    [conversations.length, activeIndex],
  )

  if (!conversations.length) {
    return (
      <div className="flex h-full items-center justify-center p-8 text-center text-sm text-muted-foreground">
        {loading ? 'Loading…' : query ? `Nothing matches “${query}”.` : 'No mail captured yet.'}
      </div>
    )
  }

  return (
    <ul className="divide-y" aria-label="Conversations" onKeyDown={onKeyDown}>
      {conversations.map((conversation, index) => {
        const unread = (conversation.unread_count ?? 0) > 0
        const selected = conversation.id === selectedId

        return (
          <li key={conversation.id}>
            <button
              type="button"
              ref={(element) => {
                rows.current[index] = element
              }}
              onClick={() => onSelect(conversation.id)}
              // Pointer focus and keyboard focus must not disagree about
              // where the single Tab stop is.
              onFocus={() => setFocusIndex(index)}
              tabIndex={index === activeIndex ? 0 : -1}
              aria-current={selected ? 'true' : undefined}
              className={`flex w-full flex-col gap-1 px-4 py-3 text-left transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:ring-inset ${
                selected ? 'bg-accent-soft' : 'hover:bg-muted'
              }`}
            >
              <div className="flex items-baseline gap-2">
                {/* The unread dot is the only always-visible cue, so it holds
                    its column whether or not it is filled. */}
                <span
                  aria-hidden
                  className={`size-2 shrink-0 rounded-full ${
                    unread ? 'bg-primary' : 'bg-transparent'
                  }`}
                />
                <span
                  className={`min-w-0 flex-1 truncate text-sm ${
                    unread ? 'font-semibold' : 'font-medium'
                  }`}
                >
                  {displayName(conversation.last_from?.[0])}
                </span>
                <span className="shrink-0 text-xs text-muted-foreground">
                  {formatWhen(conversation.last_activity_at)}
                </span>
              </div>

              <div className="flex items-center gap-2 pl-4">
                <span className={`min-w-0 flex-1 truncate text-sm ${unread ? 'font-medium' : ''}`}>
                  {conversation.subject || '(no subject)'}
                </span>
                {conversation.has_attachments && (
                  <Paperclip
                    className="size-3.5 shrink-0 text-muted-foreground"
                    aria-label="Has attachments"
                  />
                )}
                {(conversation.message_count ?? 0) > 1 && (
                  <Badge variant="outline" className="h-4 px-1.5 text-[11px] text-muted-foreground">
                    {conversation.message_count}
                  </Badge>
                )}
              </div>

              {conversation.preview && (
                <p className="truncate pl-4 text-xs text-muted-foreground">{conversation.preview}</p>
              )}
            </button>
          </li>
        )
      })}
    </ul>
  )
}
