import { Inbox } from 'lucide-react'

import type { ServerConfig } from '../types'

/**
 * What a developer sees on first run. The whole job of this screen is to make
 * the next step obvious: point a project at the capture port.
 */
export function EmptyState({ config }: { config: ServerConfig | null }) {
  const [host, port] = (config?.smtp_addr ?? '127.0.0.1:1025').split(':')

  return (
    <div className="flex h-full items-center justify-center p-8">
      <div className="max-w-md text-center">
        <Inbox className="mx-auto size-10 text-[var(--color-ink-muted)]" aria-hidden />
        <h2 className="mt-4 text-base font-semibold">Waiting for mail</h2>
        <p className="mt-1 text-sm text-[var(--color-ink-muted)]">
          Point a local project at the capture server and send something. Any credentials are
          accepted, and nothing is ever relayed onward.
        </p>

        <pre className="mt-4 overflow-x-auto rounded-lg border border-[var(--color-line)] bg-[var(--color-surface-muted)] p-4 text-left font-mono text-xs leading-relaxed">
{`MAIL_MAILER=smtp
MAIL_HOST=${host}
MAIL_PORT=${port}
MAIL_ENCRYPTION=null`}
        </pre>

        {config && !config.webhook.enabled && (
          <p className="mt-4 text-xs text-[var(--color-ink-muted)]">
            No webhook is configured yet, so replies will not reach your app. Add one in{' '}
            <code className="rounded bg-[var(--color-surface-muted)] px-1 py-0.5">
              {config.home}/config.json
            </code>
            .
          </p>
        )}
      </div>
    </div>
  )
}
