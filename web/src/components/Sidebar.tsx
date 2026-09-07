import { PenSquare, Trash2 } from 'lucide-react'

import { ConversationList } from '@/components/ConversationList'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import type { ConversationSummary } from '@/types'

interface Props {
  conversations: ConversationSummary[]
  selectedId: number | null
  loading: boolean
  query: string
  onSelect: (id: number) => void
  onCompose: () => void
  onClearMailbox: () => void
}

/**
 * The left column: what you can write above the list, what you can empty
 * below it. Both actions belong beside the mail they act on rather than in
 * the top bar, where the destructive one sat next to the theme toggle.
 */
export function Sidebar({
  conversations,
  selectedId,
  loading,
  query,
  onSelect,
  onCompose,
  onClearMailbox,
}: Props) {
  return (
    <aside className="flex w-80 shrink-0 flex-col border-r">
      <div className="border-b p-3">
        <Button size="lg" className="w-full" onClick={onCompose}>
          <PenSquare aria-hidden />
          New message
        </Button>
      </div>

      {/* The scroll lives here rather than on the aside, so the two action
          rows stay pinned while the list moves under them. */}
      <ScrollArea className="min-h-0 flex-1">
        <ConversationList
          conversations={conversations}
          selectedId={selectedId}
          loading={loading}
          query={query}
          onSelect={onSelect}
        />
      </ScrollArea>

      <div className="border-t p-2">
        <AlertDialog>
          {/* A search that matches nothing is not an empty mailbox, so the
              query has to be part of the disabled test. */}
          <AlertDialogTrigger asChild>
            <Button
              variant="ghost"
              size="lg"
              disabled={conversations.length === 0 && !query}
              className="w-full text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
            >
              <Trash2 aria-hidden />
              Delete all mail
            </Button>
          </AlertDialogTrigger>

          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Delete every captured message?</AlertDialogTitle>
              <AlertDialogDescription>
                This empties the mailbox for good. Nothing here has been relayed anywhere, so
                there is no other copy.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction variant="destructive" onClick={onClearMailbox}>
                Delete all mail
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
    </aside>
  )
}
