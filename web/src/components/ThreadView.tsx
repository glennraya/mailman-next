import { Trash2 } from 'lucide-react'

import { MessageCard } from './MessageCard'
import type { ConversationDetail } from '../types'

interface Props {
  detail: ConversationDetail
  expanded: Set<string>
  onToggleMessage: (id: string) => void
  onDeleteMessage: (id: string) => void
  onDeleteConversation: (id: number) => void
}

export function ThreadView({
  detail,
  expanded,
  onToggleMessage,
  onDeleteMessage,
  onDeleteConversation,
}: Props) {
  const { conversation, messages } = detail

  return (
    <div className="flex h-full flex-col">
      <header className="flex items-center gap-3 border-b border-[var(--color-line)] px-6 py-4">
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-base font-semibold">
            {conversation.subject || '(no subject)'}
          </h2>
          <p className="text-xs text-[var(--color-ink-muted)]">
            {messages.length} {messages.length === 1 ? 'message' : 'messages'}
          </p>
        </div>

        <button
          type="button"
          onClick={() => onDeleteConversation(conversation.id)}
          title="Delete this conversation"
          className="rounded p-2 text-[var(--color-ink-muted)] hover:bg-red-500/10 hover:text-red-500"
        >
          <Trash2 className="size-4" aria-hidden />
        </button>
      </header>

      <div className="flex-1 space-y-3 overflow-y-auto p-4">
        {messages.map((message) => (
          <MessageCard
            key={message.id}
            message={message}
            expanded={expanded.has(message.id)}
            onToggle={() => onToggleMessage(message.id)}
            onDelete={onDeleteMessage}
          />
        ))}
      </div>
    </div>
  )
}
