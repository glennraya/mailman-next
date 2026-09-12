import { useEffect, useId, useState } from 'react'
import { Loader2, Send } from 'lucide-react'

import { api } from '@/api'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { formatAddress } from '@/format'
import type { Message, ReplyResult, RouteLookup } from '@/types'

interface Props {
  open: boolean
  onClose: () => void
  /** Seeds a reply, and threads it: the server answers this message. */
  replyTo?: Message
  onSent: (result: ReplyResult) => void
}

/**
 * Writing half of the inbox.
 *
 * The draft lives here rather than in the dialog's content, which Radix
 * unmounts on close. App keeps this component mounted, so closing the modal
 * costs nothing.
 */
export function ComposeModal({ open, onClose, replyTo, onSent }: Props) {
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [cc, setCc] = useState('')
  const [bcc, setBcc] = useState('')
  const [subject, setSubject] = useState('')
  const [body, setBody] = useState('')
  const [showCopies, setShowCopies] = useState(false)
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [route, setRoute] = useState<RouteLookup | null>(null)
  const bodyId = useId()

  // Opening a reply seeds the form. A blank compose is left alone, so a draft
  // survives closing and reopening the modal.
  //
  // This mirrors what outbound.NewReply does on the server, which recomputes
  // the same fields for any the request leaves empty. It is a preview, not the
  // authority -- the client's reply-prefix test is narrower than the server's,
  // which also knows Fwd:, Re[2]: and the localized spellings.
  useEffect(() => {
    if (!open || !replyTo) return

    const sender = replyTo.reply_to?.[0] ?? replyTo.from?.[0]
    const recipient = replyTo.to?.[0]
    setTo(sender ? formatAddress(sender) : '')
    setFrom(recipient ? formatAddress(recipient) : '')
    setSubject(replyTo.subject.match(/^re:/i) ? replyTo.subject : `Re: ${replyTo.subject}`)
  }, [open, replyTo])

  // Where this will be delivered, asked as the recipient is typed. Debounced
  // the way the inbox search is, so a request is not sent per keystroke.
  const recipient = recipients(to)[0] ?? ''
  useEffect(() => {
    if (!open || !recipient) {
      setRoute(null)
      return
    }

    const timer = window.setTimeout(() => {
      api
        .route(recipient)
        .then(setRoute)
        .catch(() => setRoute(null))
    }, 250)

    return () => window.clearTimeout(timer)
  }, [open, recipient])

  // The same three rules the server enforces before it builds the message.
  const ready = from.trim() !== '' && recipients(to).length > 0 && body.trim() !== ''

  const send = async () => {
    setSending(true)
    setError(null)

    try {
      const result = await api.reply({
        parent_id: replyTo?.id,
        from: from.trim(),
        to: recipients(to),
        cc: recipients(cc),
        bcc: recipients(bcc),
        subject,
        text: body,
      })

      // Sent. Clear the draft before closing, so the next compose starts
      // empty rather than holding a message that has already gone.
      setFrom('')
      setTo('')
      setCc('')
      setBcc('')
      setSubject('')
      setBody('')
      setShowCopies(false)
      setRoute(null)

      onSent(result)
      onClose()
    } catch (err) {
      // The draft is kept: whatever went wrong, retyping the message is not
      // part of the fix.
      setError(err instanceof Error ? err.message : 'Could not send')
    } finally {
      setSending(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
    >
      <DialogContent className="max-h-[85vh] gap-0 overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{replyTo ? 'Reply' : 'New message'}</DialogTitle>
          <DialogDescription className="sr-only">
            Compose a message and deliver it to the app under test.
          </DialogDescription>
        </DialogHeader>

        <div
          className="mt-4 space-y-3"
          onKeyDown={(event) => {
            // The one shortcut a mail composer is expected to have.
            if (!(event.metaKey || event.ctrlKey) || event.key !== 'Enter') return
            if (!ready || sending) return
            event.preventDefault()
            void send()
          }}
        >
          <Field label="From" value={from} onChange={setFrom} placeholder="you@app.test" />
          <Field label="To" value={to} onChange={setTo} placeholder="someone@example.test" />

          <RoutePreview route={route} recipient={recipient} />

          {showCopies ? (
            <>
              <Field label="Cc" value={cc} onChange={setCc} />
              <Field label="Bcc" value={bcc} onChange={setBcc} />
            </>
          ) : (
            <Button
              variant="link"
              size="xs"
              className="px-0 text-muted-foreground"
              onClick={() => setShowCopies(true)}
            >
              Add Cc and Bcc
            </Button>
          )}

          <Field label="Subject" value={subject} onChange={setSubject} />

          <div className="space-y-1">
            <Label htmlFor={bodyId} className="text-xs text-muted-foreground">
              Message
            </Label>
            <Textarea
              id={bodyId}
              value={body}
              onChange={(event) => setBody(event.target.value)}
              rows={10}
              className="resize-y"
            />
          </div>
        </div>

        <DialogFooter className="mt-4 sm:items-center sm:justify-between">
          <p className={`text-xs ${error ? 'text-destructive' : 'text-muted-foreground'}`}>
            {error ?? (ready ? '' : 'A sender, a recipient and a message are required.')}
          </p>
          <div className="flex items-center gap-2">
            <DialogClose asChild>
              <Button variant="ghost" disabled={sending}>
                Cancel
              </Button>
            </DialogClose>
            <Button disabled={!ready || sending} onClick={() => void send()}>
              {sending ? <Loader2 className="animate-spin" aria-hidden /> : <Send aria-hidden />}
              {sending ? 'Sending' : 'Send'}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * Where this reply will land, before it is sent.
 *
 * A reply to an address no route covers is stored and never forwarded. Saying
 * so here, rather than after the fact, is the difference between a tool that
 * is honest and one that looks broken.
 */
function RoutePreview({ route, recipient }: { route: RouteLookup | null; recipient: string }) {
  if (!recipient || !route) return null

  if (!route.routed || !route.route) {
    return (
      <p className="pl-19 text-xs text-foreground">
        {route.reason ?? 'No webhook route matches this address'} — the reply will be stored but not
        forwarded.
      </p>
    )
  }

  return (
    <p className="pl-19 text-xs text-muted-foreground">
      Delivered as <span className="font-medium">{route.route.format}</span> to{' '}
      <span className="font-mono">{route.route.url}</span>
      {route.route.fallback && ' (the default route)'}
    </p>
  )
}

/** Splits an address field the way a person types it. */
function recipients(value: string): string[] {
  return value
    .split(',')
    .map((entry) => entry.trim())
    .filter(Boolean)
}

function Field({
  label,
  value,
  onChange,
  placeholder,
}: {
  label: string
  value: string
  onChange: (value: string) => void
  placeholder?: string
}) {
  const id = useId()

  return (
    <div className="flex items-center gap-3">
      {/* A visible label rather than a placeholder: the placeholder vanishes
          the moment there is something to check it against. */}
      <Label htmlFor={id} className="w-16 shrink-0 text-xs text-muted-foreground">
        {label}
      </Label>
      <Input
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
      />
    </div>
  )
}
