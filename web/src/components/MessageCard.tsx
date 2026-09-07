import { useState } from 'react'
import {
  ChevronDown,
  Download,
  FileText,
  Image as ImageIcon,
  ImageOff,
  Paperclip,
  Trash2,
} from 'lucide-react'

import { api } from '@/api'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { displayName, formatAddressList, formatBytes, formatExactWhen, initials } from '@/format'
import type { Message } from '@/types'

interface Props {
  message: Message
  expanded: boolean
  onToggle: () => void
  onDelete: (id: string) => void
}

export function MessageCard({ message, expanded, onToggle, onDelete }: Props) {
  const [blockImages, setBlockImages] = useState(false)
  const [tab, setTab] = useState<'html' | 'text'>(message.html_body ? 'html' : 'text')

  const sender = message.from?.[0]
  // Inline parts belong to the body; listing them again as downloads is noise.
  const files = message.attachments?.filter((a) => !a.inline) ?? []
  const hasHeaderExtras =
    Boolean(message.cc?.length) || Boolean(message.bcc?.length) || Boolean(message.reply_to?.length)

  return (
    <Collapsible open={expanded} onOpenChange={onToggle} asChild>
      <Card className="gap-0 py-0">
        {/* The trigger is a real button, so its contents are spans rather than
            divs and paragraphs: Enter, Space and the focus ring all come free
            from the element instead of being re-implemented on a div. */}
        <CollapsibleTrigger asChild>
          <button
            type="button"
            className="flex w-full items-start gap-3 p-4 text-left outline-none focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:ring-inset"
          >
            <Avatar className="size-9">
              <AvatarFallback className="bg-accent-soft text-xs font-semibold text-foreground">
                {initials(sender)}
              </AvatarFallback>
            </Avatar>

            <span className="min-w-0 flex-1">
              <span className="flex items-baseline gap-2">
                <span className="truncate text-sm font-medium">{displayName(sender)}</span>
                {message.direction === 'outbound' && (
                  <Badge variant="outline" className="h-4 px-1.5 text-[11px] text-muted-foreground">
                    sent
                  </Badge>
                )}
                <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                  {formatExactWhen(message.sent_at || message.created_at)}
                </span>
              </span>

              <span className="block truncate text-xs text-muted-foreground">
                to {formatAddressList(message.to) || '(nobody)'}
              </span>
            </span>

            <ChevronDown
              className={`size-4 shrink-0 text-muted-foreground transition-transform ${
                expanded ? 'rotate-180' : ''
              }`}
              aria-hidden
            />
          </button>
        </CollapsibleTrigger>

        <CollapsibleContent className="border-t">
          {hasHeaderExtras && (
            <dl className="space-y-0.5 border-b px-4 py-3 text-xs text-muted-foreground">
              {message.cc?.length ? <Row label="Cc" value={formatAddressList(message.cc)} /> : null}
              {/* Bcc is reconstructed from the envelope; it appears in no
                  header, and seeing it is often the reason to open a message. */}
              {message.bcc?.length ? (
                <Row label="Bcc" value={formatAddressList(message.bcc)} />
              ) : null}
              {message.reply_to?.length ? (
                <Row label="Reply-To" value={formatAddressList(message.reply_to)} />
              ) : null}
            </dl>
          )}

          <Tabs
            value={tab}
            onValueChange={(value) => setTab(value as 'html' | 'text')}
            className="gap-0"
          >
            <div className="flex flex-wrap items-center gap-1 px-4 py-2">
              <TabsList variant="line">
                {message.html_body && <TabsTrigger value="html">HTML</TabsTrigger>}
                <TabsTrigger value="text">Text</TabsTrigger>
              </TabsList>

              <div className="ml-auto flex items-center gap-1">
                {tab === 'html' && (
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <Button
                        variant="ghost"
                        size="xs"
                        className="text-muted-foreground"
                        onClick={() => setBlockImages((v) => !v)}
                      >
                        {blockImages ? (
                          <ImageIcon aria-hidden />
                        ) : (
                          <ImageOff aria-hidden />
                        )}
                        {blockImages ? 'Show images' : 'Block images'}
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent>
                      {blockImages
                        ? 'Remote images are blocked, so opening this cannot tell its sender you read it'
                        : 'Remote images load by default; block them to stop a tracking pixel firing'}
                    </TooltipContent>
                  </Tooltip>
                )}

                <Button variant="ghost" size="xs" className="text-muted-foreground" asChild>
                  <a href={api.rawUrl(message.id)} target="_blank" rel="noreferrer">
                    <FileText aria-hidden />
                    Source
                  </a>
                </Button>

                <Button
                  variant="ghost"
                  size="xs"
                  className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                  onClick={() => onDelete(message.id)}
                >
                  <Trash2 aria-hidden />
                  Delete
                </Button>
              </div>
            </div>

            {message.html_body && (
              <TabsContent value="html">
                <iframe
                  // No allow-scripts: the body is untrusted, and the server's
                  // Content-Security-Policy forbids scripts too. Either alone
                  // would do; both means one mistake is not a hole.
                  sandbox="allow-same-origin allow-popups"
                  src={api.htmlUrl(message.id, blockImages)}
                  title={`Message ${message.id}`}
                  className="h-[28rem] w-full border-0 bg-white dark:bg-card"
                />
              </TabsContent>
            )}

            <TabsContent value="text">
              <pre className="max-h-[28rem] overflow-auto px-4 pb-4 font-mono text-[13px] leading-relaxed whitespace-pre-wrap">
                {message.text_body || '(this message has no text body)'}
              </pre>
            </TabsContent>
          </Tabs>

          {files.length > 0 && (
            <div className="flex flex-wrap gap-2 border-t px-4 py-3">
              {files.map((file) => (
                <Button key={file.id} variant="outline" size="sm" asChild>
                  <a href={api.attachmentUrl(file.id)}>
                    <Paperclip className="text-muted-foreground" aria-hidden />
                    <span className="max-w-48 truncate">{file.filename}</span>
                    <span className="text-muted-foreground">{formatBytes(file.size_bytes)}</span>
                    <Download className="text-muted-foreground" aria-hidden />
                  </a>
                </Button>
              ))}
            </div>
          )}
        </CollapsibleContent>
      </Card>
    </Collapsible>
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
