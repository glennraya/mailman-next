import { useEffect, useRef, useState } from 'react'
import type { MailboxEvent } from './types'

type Status = 'connecting' | 'live' | 'offline'

// Backoff for reconnects. Starts fast because the usual cause is the Go
// server restarting under a rebuild, and caps low because this is a local
// tool -- there is no upstream to be gentle with.
const FIRST_RETRY_MS = 500
const MAX_RETRY_MS = 10_000

interface Options {
  onEvent: (event: MailboxEvent) => void
}

/**
 * Holds one WebSocket to the server and hands every mailbox change to the
 * caller. Reconnects on its own, so a Go rebuild during development shows up
 * as a brief "reconnecting" rather than a dead page.
 */
export function useMailboxEvents({ onEvent }: Options): Status {
  const [status, setStatus] = useState<Status>('connecting')

  // The callback is held in a ref so a re-render with a new closure does not
  // tear down and rebuild the socket.
  const handler = useRef(onEvent)
  handler.current = onEvent

  useEffect(() => {
    let socket: WebSocket | null = null
    let retryTimer: number | undefined
    let retryDelay = FIRST_RETRY_MS
    let closed = false

    const connect = () => {
      if (closed) return

      const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
      socket = new WebSocket(`${protocol}//${location.host}/api/v1/events`)

      socket.onopen = () => {
        retryDelay = FIRST_RETRY_MS
        setStatus('live')
      }

      socket.onmessage = (event) => {
        try {
          handler.current(JSON.parse(event.data as string) as MailboxEvent)
        } catch {
          // A frame we cannot read is not worth breaking the stream over.
        }
      }

      socket.onclose = () => {
        if (closed) return
        setStatus('offline')
        retryTimer = window.setTimeout(connect, retryDelay)
        retryDelay = Math.min(retryDelay * 2, MAX_RETRY_MS)
      }

      // An error is always followed by a close, which is where the retry
      // lives; this just keeps the console quiet.
      socket.onerror = () => socket?.close()
    }

    connect()

    return () => {
      closed = true
      window.clearTimeout(retryTimer)
      socket?.close()
    }
  }, [])

  return status
}
