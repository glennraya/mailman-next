import { Trash2 } from 'lucide-react'

import { MessageCard } from '@/components/MessageCard'
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
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { ConversationDetail } from '@/types'

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
      <header className="flex items-center gap-3 border-b px-6 py-4">
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-base font-semibold">
            {conversation.subject || '(no subject)'}
          </h2>
          <p className="text-xs text-muted-foreground">
            {messages.length} {messages.length === 1 ? 'message' : 'messages'}
          </p>
        </div>

        <AlertDialog>
          <Tooltip>
            <TooltipTrigger asChild>
              <AlertDialogTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label="Delete this conversation"
                  className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                >
                  <Trash2 aria-hidden />
                </Button>
              </AlertDialogTrigger>
            </TooltipTrigger>
            <TooltipContent>Delete this conversation</TooltipContent>
          </Tooltip>

          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Delete this conversation?</AlertDialogTitle>
              <AlertDialogDescription>
                All {messages.length} {messages.length === 1 ? 'message' : 'messages'} in “
                {conversation.subject || '(no subject)'}” go with it, and there is no other copy.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                onClick={() => onDeleteConversation(conversation.id)}
              >
                Delete conversation
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </header>

      <ScrollArea className="min-h-0 flex-1">
        <div className="space-y-3 p-4">
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
      </ScrollArea>
    </div>
  )
}
