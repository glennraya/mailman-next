import { Paperclip } from 'lucide-react'

import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import { Kbd, KbdGroup } from '@/components/ui/kbd'
import { displayName, formatWhen } from '@/format'
import type { ConversationSummary } from '@/types'

interface Props {
  open: boolean
  onClose: () => void
  query: string
  onQueryChange: (query: string) => void
  results: ConversationSummary[]
  loading: boolean
  /** The server capped the result set; the client does not page yet. */
  hasMore: boolean
  onSelect: (id: number) => void
}

/**
 * The search palette. It binds to the query the app already owns rather than
 * fetching for itself, so the debounce, the list behind it and the empty
 * state all stay in one place.
 *
 * shouldFilter is off because the matching happens in SQLite: cmdk is here for
 * the keyboard and the combobox semantics, not to filter a second time over
 * whatever subset the server chose to return.
 */
export function SearchModal({
  open,
  onClose,
  query,
  onQueryChange,
  results,
  loading,
  hasMore,
  onSelect,
}: Props) {
  return (
    <CommandDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
      title="Search mail"
      description="Search subjects, bodies and addresses"
      // CommandDialog anchors itself a third of the way down the viewport.
      // This puts it back in the middle.
      className="top-1/2 -translate-y-1/2 sm:max-w-xl"
    >
      <Command shouldFilter={false}>
        <CommandInput
          value={query}
          onValueChange={onQueryChange}
          placeholder="Search subjects, bodies and addresses"
        />

        <CommandList>
          <CommandEmpty>
            {loading ? 'Searching…' : query ? `Nothing matches “${query}”.` : 'No mail captured yet.'}
          </CommandEmpty>

          {results.map((conversation) => (
            <CommandItem
              key={conversation.id}
              // Subjects collide, so selection keys off the id instead.
              value={String(conversation.id)}
              onSelect={() => {
                onSelect(conversation.id)
                onClose()
              }}
              // CommandItem reserves a trailing checkmark for pickers. This
              // is not one, and the gap it leaves pulls the rows apart.
              className="flex-col items-stretch gap-0.5 [&>svg]:hidden"
            >
              <div className="flex items-baseline gap-2">
                <span className="min-w-0 flex-1 truncate font-medium">
                  {conversation.subject || '(no subject)'}
                </span>
                {conversation.has_attachments && (
                  <Paperclip className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                )}
                <span className="shrink-0 text-xs text-muted-foreground">
                  {formatWhen(conversation.last_activity_at)}
                </span>
              </div>
              <p className="truncate text-xs text-muted-foreground">
                {displayName(conversation.last_from?.[0])}
                {conversation.preview ? ` — ${conversation.preview}` : ''}
              </p>
            </CommandItem>
          ))}
        </CommandList>

        <p aria-live="polite" className="sr-only">
          {results.length} {results.length === 1 ? 'result' : 'results'}
        </p>

        <footer className="mt-1 flex items-center gap-3 border-t px-3 pt-2 pb-1 text-[11px] text-muted-foreground">
          <span className="flex items-center gap-1">
            <KbdGroup>
              <Kbd>↑</Kbd>
              <Kbd>↓</Kbd>
            </KbdGroup>
            navigate
          </span>
          <span className="flex items-center gap-1">
            <Kbd>↵</Kbd> open
          </span>
          <span className="flex items-center gap-1">
            <Kbd>esc</Kbd> close
          </span>
          {hasMore && <span className="ml-auto">Showing the first 50 matches</span>}
        </footer>
      </Command>
    </CommandDialog>
  )
}
