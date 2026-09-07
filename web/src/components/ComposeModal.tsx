import { useEffect, useId, useState } from 'react'

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
import type { Message } from '@/types'

interface Props {
  open: boolean
  onClose: () => void
  /** Seeds a reply. Delivery is the next change; the shape is here already. */
  replyTo?: Message
}

/**
 * Writing half of the inbox. The fields and their validation match what
 * POST /api/v1/messages already accepts, so wiring delivery to this form is
 * an addition rather than a rewrite -- but the button stays disabled until
 * that endpoint sends rather than re-captures.
 *
 * The draft lives here rather than in the dialog's content, which Radix
 * unmounts on close. App keeps this component mounted, so closing the modal
 * costs nothing.
 */
export function ComposeModal({ open, onClose, replyTo }: Props) {
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [cc, setCc] = useState('')
  const [bcc, setBcc] = useState('')
  const [subject, setSubject] = useState('')
  const [body, setBody] = useState('')
  const [showCopies, setShowCopies] = useState(false)
  const bodyId = useId()

  // Opening a reply seeds the form. A blank compose is left alone, so a draft
  // survives closing and reopening the modal.
  useEffect(() => {
    if (!open || !replyTo) return

    const sender = replyTo.reply_to?.[0] ?? replyTo.from?.[0]
    const recipient = replyTo.to?.[0]
    setTo(sender ? formatAddress(sender) : '')
    setFrom(recipient ? formatAddress(recipient) : '')
    setSubject(replyTo.subject.match(/^re:/i) ? replyTo.subject : `Re: ${replyTo.subject}`)
  }, [open, replyTo])

  // The same three rules the server enforces in buildInjected.
  const ready = from.trim() !== '' && recipients(to).length > 0 && body.trim() !== ''

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
            Compose a message against the capture mailbox.
          </DialogDescription>
        </DialogHeader>

        <div className="mt-4 space-y-3">
          <Field label="From" value={from} onChange={setFrom} placeholder="you@app.test" />
          <Field label="To" value={to} onChange={setTo} placeholder="someone@example.test" />

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
          <p className="text-xs text-muted-foreground">
            {ready
              ? 'Ready to send once delivery lands.'
              : 'A sender, a recipient and a message are required.'}
          </p>
          <div className="flex items-center gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button disabled title="Sending arrives with the reply and webhook delivery work">
              Send
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
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
