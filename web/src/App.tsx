import { useCallback, useEffect, useRef, useState } from 'react'
import { Moon, Search, Settings2, Sun, X } from 'lucide-react'

import { api } from '@/api'
import { ComposeModal } from '@/components/ComposeModal'
import { EmptyState } from '@/components/EmptyState'
import { SearchModal } from '@/components/SearchModal'
import { SettingsModal } from '@/components/SettingsModal'
import { Sidebar } from '@/components/Sidebar'
import { ThreadView } from '@/components/ThreadView'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Kbd, KbdGroup } from '@/components/ui/kbd'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useMailboxEvents } from '@/useMailboxEvents'
import { useTheme } from '@/useTheme'
import type {
  ConversationDetail,
  ConversationSummary,
  MailboxEvent,
  Message,
  ReplyResult,
  ServerConfig,
} from '@/types'

export default function App() {
  const [conversations, setConversations] = useState<ConversationSummary[]>([])
  const [detail, setDetail] = useState<ConversationDetail | null>(null)
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [unread, setUnread] = useState(0)
  const [query, setQuery] = useState('')
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [config, setConfig] = useState<ServerConfig | null>(null)
  const [searchOpen, setSearchOpen] = useState(false)
  const [composeOpen, setComposeOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  // Bumped on a config.changed event, so a save in another tab reaches an
  // open settings dialog.
  const [settingsRevision, setSettingsRevision] = useState(0)
  const [replyTo, setReplyTo] = useState<Message | undefined>(undefined)
  // Bumped on every delivery.completed, which is how an open delivery log
  // refetches itself after a retry from another tab.
  const [deliveryRevision, setDeliveryRevision] = useState(0)
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
      // The client does not page yet, so a capped result set is something the
      // search palette has to admit to rather than hide.
      setHasMore(list.next_cursor !== undefined)
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
    api
      .config()
      .then(setConfig)
      .catch(() => setConfig(null))
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

      if (event.type === 'delivery.completed') {
        setDeliveryRevision((current) => current + 1)
      }

      // Settings are not mailbox news: refresh them and stop, rather than
      // running a conversation query for a change that cannot affect one.
      if (event.type === 'config.changed') {
        setSettingsRevision((current) => current + 1)
        void api
          .config()
          .then(setConfig)
          .catch(() => {})
        return
      }

      // The list is refetched rather than patched: it is one cheap query,
      // and it keeps ordering, previews and counts consistent without
      // duplicating the server's list logic in the client.
      void loadConversations(queryRef.current)

      // Refresh the open thread only when the change was actually in it.
      const open = selectedRef.current
      if (open !== null && event.conversation_id === open) {
        void api
          .conversation(open)
          .then(setDetail)
          .catch(() => {})
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

  // The one global shortcut. Compose keeps its own keystrokes -- a modal that
  // is already open should not lose the field under the cursor.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== 'k') return
      if (composeOpen) return

      event.preventDefault()
      setSearchOpen(true)
    }

    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [composeOpen])

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

  // The confirmation is an AlertDialog in the sidebar, so by the time this
  // runs the question has already been answered.
  const clearMailbox = useCallback(async () => {
    await api.clearMailbox()
    setSelectedId(null)
    setDetail(null)
    void loadConversations(queryRef.current)
  }, [loadConversations])

  const reply = useCallback((message: Message) => {
    setReplyTo(message)
    setComposeOpen(true)
  }, [])

  // A fresh compose must not be seeded by the last thread that was read.
  const compose = useCallback(() => {
    setReplyTo(undefined)
    setComposeOpen(true)
  }, [])

  // A reply is always stored, so nothing is lost either way. What is worth
  // interrupting for is a reply that did not reach the app -- because no route
  // covered it, or because the app refused it. The thread shows the detail;
  // this is the notice that there is detail to look at.
  const onSent = useCallback((result: ReplyResult) => {
    if (!result.routed) {
      setError(result.reason ?? 'The reply was stored but not forwarded')
      return
    }

    const delivery = result.delivery
    const refused =
      delivery && (delivery.error || (delivery.status_code ?? 0) >= 300)
    setError(
      refused
        ? `Your app did not accept the reply: ${delivery.error || delivery.status_code}`
        : null,
    )
  }, [])

  const openSettings = useCallback(() => setSettingsOpen(true), [])

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
      <header className="flex items-center gap-3 border-b px-4 py-3">
        <div className="flex items-center gap-2">
          <span className="grid size-7 place-items-center rounded-md bg-primary text-xs font-bold text-primary-foreground">
            M
          </span>
          <span className="font-semibold">Mailman</span>
          {unread > 0 && <Badge>{unread}</Badge>}
        </div>

        {/* A button rather than an input: a real field that blurs itself into
            a modal fights the screen reader for focus. */}
        <div className="mx-auto flex w-full max-w-md items-center gap-1">
          <Button
            variant="outline"
            className="min-w-0 flex-1 justify-start bg-muted font-normal text-muted-foreground"
            onClick={() => setSearchOpen(true)}
          >
            <Search aria-hidden />
            <span className={`min-w-0 flex-1 truncate text-left ${query ? 'text-foreground' : ''}`}>
              {query || 'Search mail'}
            </span>
            <KbdGroup>
              {shortcutKeys().map((key) => (
                <Kbd key={key}>{key}</Kbd>
              ))}
            </KbdGroup>
          </Button>
          {query && (
            <Button
              variant="ghost"
              size="icon"
              aria-label="Clear search"
              className="text-muted-foreground"
              onClick={() => setQuery('')}
            >
              <X aria-hidden />
            </Button>
          )}
        </div>

        <div className="flex items-center gap-1">
          <ConnectionBadge status={status} />
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label="Settings"
                className="text-muted-foreground"
                onClick={openSettings}
              >
                <Settings2 aria-hidden />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Where replies are delivered</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label={theme === 'dark' ? 'Switch to light' : 'Switch to dark'}
                className="text-muted-foreground"
                onClick={toggleTheme}
              >
                {theme === 'dark' ? <Sun aria-hidden /> : <Moon aria-hidden />}
              </Button>
            </TooltipTrigger>
            <TooltipContent>
              {theme === 'dark' ? 'Switch to light' : 'Switch to dark'}
            </TooltipContent>
          </Tooltip>
        </div>
      </header>

      {error && (
        <Alert variant="destructive" className="rounded-none border-x-0 border-t-0">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      <div className="flex min-h-0 flex-1">
        <Sidebar
          conversations={conversations}
          selectedId={selectedId}
          loading={loading}
          query={query}
          onSelect={selectConversation}
          onCompose={compose}
          onClearMailbox={clearMailbox}
        />

        <main className="min-w-0 flex-1 overflow-hidden">
          {detail ? (
            <ThreadView
              detail={detail}
              expanded={expanded}
              onToggleMessage={toggleMessage}
              onDeleteMessage={deleteMessage}
              onDeleteConversation={deleteConversation}
              onReply={reply}
              deliveryRevision={deliveryRevision}
            />
          ) : (
            <EmptyState config={config} onOpenSettings={openSettings} />
          )}
        </main>
      </div>

      {/* Both modals stay mounted so a compose draft survives being closed. */}
      <SearchModal
        open={searchOpen}
        onClose={() => setSearchOpen(false)}
        query={query}
        onQueryChange={setQuery}
        results={conversations}
        loading={loading}
        hasMore={hasMore}
        onSelect={selectConversation}
      />

      <ComposeModal
        open={composeOpen}
        onClose={() => setComposeOpen(false)}
        replyTo={replyTo}
        onSent={onSent}
        onOpenSettings={openSettings}
      />

      <SettingsModal
        open={settingsOpen}
        onClose={() => setSettingsOpen(false)}
        config={config}
        revision={settingsRevision}
        onSaved={setConfig}
      />
    </div>
  )
}

/** The modifier the reader's own keyboard uses, split into pills. */
function shortcutKeys(): string[] {
  const mac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.userAgent)
  return mac ? ['⌘', 'K'] : ['Ctrl', 'K']
}

function ConnectionBadge({ status }: { status: 'connecting' | 'live' | 'offline' }) {
  if (status === 'live') return null

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge variant="ghost" className="text-muted-foreground">
          {status === 'connecting' ? 'Connecting…' : 'Reconnecting…'}
        </Badge>
      </TooltipTrigger>
      <TooltipContent>
        The live connection dropped; new mail will appear once it is back.
      </TooltipContent>
    </Tooltip>
  )
}
