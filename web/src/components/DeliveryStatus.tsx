import { useCallback, useEffect, useState } from 'react'
import { CheckCircle2, RefreshCw, XCircle } from 'lucide-react'

import { api } from '@/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { formatExactWhen } from '@/format'
import type { Delivery, RouteInfo } from '@/types'

interface Props {
  messageId: string
  /** Bumped by a delivery.completed event, to refetch after a retry elsewhere. */
  revision: number
}

/**
 * What happened to a reply after it left. This is the half of the loop a
 * capture tool cannot show: whether the app under test actually accepted it,
 * and if it did not, what it said instead.
 */
export function DeliveryStatus({ messageId, revision }: Props) {
  const [deliveries, setDeliveries] = useState<Delivery[] | null>(null)
  const [route, setRoute] = useState<RouteInfo | undefined>(undefined)
  const [retrying, setRetrying] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      const result = await api.deliveries(messageId)
      setDeliveries(result.deliveries)
      setRoute(result.route)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not read the delivery log')
    }
  }, [messageId])

  useEffect(() => {
    void load()
  }, [load, revision])

  const retry = async () => {
    setRetrying(true)
    try {
      const result = await api.retryDelivery(messageId)
      // A reply with nowhere to go is not an error, but it is not success
      // either, and the reason is the only useful thing to show.
      setError(result.routed ? null : (result.reason ?? null))
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not retry')
    } finally {
      setRetrying(false)
    }
  }

  // Newest first from the server, so the head is the current state and the
  // tail is the original attempt.
  const latest = deliveries?.[0]
  const earliest = deliveries && deliveries.length > 1 ? deliveries[deliveries.length - 1] : null

  return (
    <div className="space-y-2 border-t px-4 py-3 text-xs">
      <div className="flex flex-wrap items-center gap-2">
        {latest ? <Outcome delivery={latest} /> : <NotForwarded route={route} />}

        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="xs"
              className="ml-auto text-muted-foreground"
              disabled={retrying}
              onClick={() => void retry()}
            >
              <RefreshCw className={retrying ? 'animate-spin' : ''} aria-hidden />
              {latest ? 'Retry' : 'Deliver'}
            </Button>
          </TooltipTrigger>
          {/* The route is resolved afresh rather than replayed, so a reply
              that was never forwarded can be delivered once a route exists.
              Editing config.json still needs a restart to take effect -- the
              reply keeps until then. */}
          <TooltipContent>
            Send this again, resolving the route from the running configuration
          </TooltipContent>
        </Tooltip>
      </div>

      {latest?.error && (
        <p className="max-h-24 overflow-auto font-mono text-[11px] leading-relaxed whitespace-pre-wrap text-muted-foreground">
          {latest.error}
        </p>
      )}

      {earliest && deliveries && (
        <p className="text-muted-foreground">
          {deliveries.length} attempts, the first {formatExactWhen(earliest.created_at)}
        </p>
      )}

      {error && <p className="text-destructive">{error}</p>}
    </div>
  )
}

function Outcome({ delivery }: { delivery: Delivery }) {
  const accepted =
    !delivery.error && delivery.status_code !== undefined && delivery.status_code < 300

  return (
    <>
      <Badge variant={accepted ? 'outline' : 'destructive'} className="gap-1">
        {accepted ? <CheckCircle2 aria-hidden /> : <XCircle aria-hidden />}
        {delivery.status_code ? String(delivery.status_code) : 'not delivered'}
      </Badge>
      <span className="min-w-0 truncate text-muted-foreground">
        {delivery.format} to {delivery.target_url}
      </span>
      <span className="shrink-0 text-muted-foreground">
        {delivery.duration_ms} ms{delivery.attempt > 1 && `, attempt ${delivery.attempt}`}
      </span>
    </>
  )
}

/**
 * No attempt was ever made. Saying so is the point: a reply that silently went
 * nowhere is the failure this whole feature exists to make visible.
 */
function NotForwarded({ route }: { route?: RouteInfo }) {
  if (route) {
    return (
      <span className="min-w-0 truncate text-muted-foreground">
        Not forwarded yet — {route.format} to {route.url}
      </span>
    )
  }

  return (
    <span className="text-muted-foreground">
      Not forwarded: no webhook route matches this recipient
    </span>
  )
}
