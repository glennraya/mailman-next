import { Inbox } from 'lucide-react'

import { Button } from '@/components/ui/button'
import type { ServerConfig } from '@/types'

/**
 * What a developer sees on first run. The whole job of this screen is to make
 * the next step obvious: point a project at the capture port.
 */
export function EmptyState({
  config,
  onOpenSettings,
}: {
  config: ServerConfig | null
  onOpenSettings: () => void
}) {
  const [host, port] = (config?.smtp_addr ?? '127.0.0.1:1983').split(':')

  return (
    <div className="flex h-full items-center justify-center p-8">
      <div className="max-w-md text-center">
        <Inbox className="mx-auto size-10 text-muted-foreground" aria-hidden />
        <h2 className="mt-4 text-base font-semibold">Waiting for mail</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Point a local project at the capture server and send something. Any credentials are
          accepted, and nothing is ever relayed onward.
        </p>

        <pre className="mt-4 overflow-x-auto rounded-lg border bg-muted p-4 text-left font-mono text-xs leading-relaxed">
{`MAIL_MAILER=smtp
MAIL_HOST=${host}
MAIL_PORT=${port}
MAIL_ENCRYPTION=null`}
        </pre>

        {config && !config.webhook.enabled && (
          <div className="mt-5 space-y-2">
            <p className="text-xs text-muted-foreground">
              No webhook is configured yet, so replies will not reach your app.
            </p>
            <Button variant="outline" size="sm" onClick={onOpenSettings}>
              Set up a webhook
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}
