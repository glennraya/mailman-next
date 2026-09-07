import { useCallback, useEffect, useRef, useState } from 'react'
import { Moon, Search, Sun, Trash2 } from 'lucide-react'

import { api } from './api'
import { ConversationList } from './components/ConversationList'
import { EmptyState } from './components/EmptyState'
import { ThreadView } from './components/ThreadView'
import { useMailboxEvents } from './useMailboxEvents'
import { useTheme } from './useTheme'
import type { ConversationDetail, ConversationSummary, MailboxEvent, ServerConfig } from './types'

export default function App() {
  const [conversations, setConversations] = useState<ConversationSummary[]>([])
  const [detail, setDetail] = useState<ConversationDetail | null>(null)
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [unread, setUnread] = useState(0)
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [config, setConfig] = useState<ServerConfig | null>(null)
  const [theme, toggleTheme] = useTheme()

  // Held in a ref so the event handler can read the current selection
  // without being rebuilt -- and thus without cycling the WebSocket.
  const selectedRef = useRef<number | null>(null)
  selectedRef.current = selectedId

  const queryRef = useRef(query)
  queryRef.current = query

  const loadConversations = useCallback(async (search: string) => {
    try {
      const list = await api.conversations(search)
      setConversations(list.conversations)
      setUnread(list.unread)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not load the inbox')
    } finally {
      setLoading(false)
    }
  }, [])

  const loadThread = useCallback(async (id: number) => {
    try {
      const found = await api.conversation(id)
      setDetail(found)
      // Opening a thread expands its newest message, which is what the
      // reader almost always wants to see first.
      const newest = found.messages.at(-1)
      setExpanded(new Set(newest ? [newest.id] : []))

      const { unread: remaining } = await api.markConversationSeen(id)
      setUnread(remaining)
      setConversations((current) =>
        current.map((c) => (c.id === id ? { ...c, unread_count: 0 } : c)),
      )
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not open the conversation')
    }
  }, [])

  useEffect(() => {
    api.config().then(setConfig).catch(() => setConfig(null))
  }, [])

  // Search is debounced so a query is not sent on every keystroke.
  useEffect(() => {
    const timer = window.setTimeout(() => void loadConversations(query), 200)
    return () => window.clearTimeout(timer)
  }, [query, loadConversations])

  const onEvent = useCallback(
    (event: MailboxEvent) => {
      setUnread(event.unread)

      if (event.type === 'hello') return

      if (event.type === 'mailbox.cleared') {
        setSelectedId(null)
        setDetail(null)
      }

      // The list is refetched rather than patched: it is one cheap query,
      // and it keeps ordering, previews and counts consistent without
      // duplicating the server's list logic in the client.
      void loadConversations(queryRef.current)

      // Refresh the open thread only when the change was actually in it.
      const open = selectedRef.current
      if (open !== null && event.conversation_id === open) {
        void api.conversation(open).then(setDetail).catch(() => {})
      }
    },
    [loadConversations],
  )

  const status = useMailboxEvents({ onEvent })

  // The tab title is the notification a developer sees while working in
  // another window.
  useEffect(() => {
    document.title = unread > 0 ? `(${unread}) Mailman` : 'Mailman'
  }, [unread])

  const selectConversation = useCallback(
    (id: number) => {
      setSelectedId(id)
      void loadThread(id)
    },
    [loadThread],
  )

  const deleteMessage = useCallback(
    async (id: string) => {
      const result = await api.deleteMessage(id)
      if (result.conversation_deleted) {
        setSelectedId(null)
        setDetail(null)
      } else if (selectedRef.current !== null) {
        setDetail(await api.conversation(selectedRef.current))
      }
      void loadConversations(queryRef.current)
    },
    [loadConversations],
  )

  const deleteConversation = useCallback(
    async (id: number) => {
      await api.deleteConversation(id)
      setSelectedId(null)
      setDetail(null)
      void loadConversations(queryRef.current)
    },
    [loadConversations],
  )

  const clearMailbox = useCallback(async () => {
    if (!confirm('Delete every captured message? This cannot be undone.')) return
    await api.clearMailbox()
    setSelectedId(null)
    setDetail(null)
    void loadConversations(queryRef.current)
  }, [loadConversations])

  const toggleMessage = useCallback((id: string) => {
    setExpanded((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  return (
    <div className="flex h-full flex-col">
      <header className="flex items-center gap-3 border-b border-[var(--color-line)] px-4 py-3">
        <div className="flex items-center gap-2">
          <span className="grid size-7 place-items-center rounded-md bg-[var(--color-accent)] text-xs font-bold text-white">
            M
          </span>
          <span className="font-semibold">Mailman</span>
          {unread > 0 && (
            <span className="rounded-full bg-[var(--color-accent)] px-2 py-0.5 text-xs font-medium text-white">
              {unread}
            </span>
          )}
        </div>

        <div className="relative mx-auto w-full max-w-md">
          <Search
            className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-[var(--color-ink-muted)]"
            aria-hidden
          />
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search subjects, bodies and addresses"
            aria-label="Search mail"
            className="w-full rounded-md border border-[var(--color-line)] bg-[var(--color-surface-muted)] py-1.5 pr-3 pl-8 text-sm outline-none focus:border-[var(--color-accent)]"
          />
        </div>

        <div className="flex items-center gap-1">
          <ConnectionBadge status={status} />
          <button
            type="button"
            onClick={clearMailbox}
            title="Delete every captured message"
            className="rounded p-2 text-[var(--color-ink-muted)] hover:bg-red-500/10 hover:text-red-500"
          >
            <Trash2 className="size-4" aria-hidden />
          </button>
          <button
            type="button"
            onClick={toggleTheme}
            title={theme === 'dark' ? 'Switch to light' : 'Switch to dark'}
            className="rounded p-2 text-[var(--color-ink-muted)] hover:bg-[var(--color-surface-muted)]"
          >
            {theme === 'dark' ? <Sun className="size-4" aria-hidden /> : <Moon className="size-4" aria-hidden />}
          </button>
        </div>
      </header>

      {error && (
        <div className="border-b border-red-500/30 bg-red-500/10 px-4 py-2 text-sm text-red-600 dark:text-red-400">
          {error}
        </div>
      )}

      <div className="flex min-h-0 flex-1">
        <aside className="w-80 shrink-0 overflow-y-auto border-r border-[var(--color-line)]">
          <ConversationList
            conversations={conversations}
            selectedId={selectedId}
            loading={loading}
            query={query}
            onSelect={selectConversation}
          />
        </aside>

        <main className="min-w-0 flex-1 overflow-hidden">
          {detail ? (
            <ThreadView
              detail={detail}
              expanded={expanded}
              onToggleMessage={toggleMessage}
              onDeleteMessage={deleteMessage}
              onDeleteConversation={deleteConversation}
            />
          ) : (
            <EmptyState config={config} />
          )}
        </main>
      </div>
    </div>
  )
}

function ConnectionBadge({ status }: { status: 'connecting' | 'live' | 'offline' }) {
  if (status === 'live') return null

  return (
    <span
      className="rounded px-2 py-1 text-xs text-[var(--color-ink-muted)]"
      title="The live connection dropped; new mail will appear once it is back."
    >
      {status === 'connecting' ? 'Connecting…' : 'Reconnecting…'}
    </span>
  )
}
