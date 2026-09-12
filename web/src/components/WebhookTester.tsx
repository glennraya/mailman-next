import { useState } from 'react'
import { CheckCircle2, Loader2, XCircle, Zap } from 'lucide-react'

import { api } from '@/api'
import { Button } from '@/components/ui/button'
import type { WebhookTest, WebhookTestResult } from '@/types'

interface Props {
  target: () => WebhookTest
  disabled?: boolean
}

/**
 * Proves a URL before a real reply depends on it.
 *
 * It posts a synthetic message in the chosen format and reports what came
 * back, which is the fastest way to find the things that actually go wrong:
 * an app that is not running, a route behind CSRF, a handler that rejects the
 * payload shape. Nothing is recorded and nothing is saved.
 */
export function WebhookTester({ target, disabled }: Props) {
  const [testing, setTesting] = useState(false)
  const [result, setResult] = useState<WebhookTestResult | null>(null)
  const [error, setError] = useState<string | null>(null)

  const run = async () => {
    setTesting(true)
    setError(null)
    setResult(null)

    try {
      setResult(await api.testWebhook(target()))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not run the test')
    } finally {
      setTesting(false)
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-2 text-xs">
      <Button
        variant="outline"
        size="xs"
        disabled={disabled || testing}
        onClick={() => void run()}
      >
        {testing ? <Loader2 className="animate-spin" aria-hidden /> : <Zap aria-hidden />}
        Test
      </Button>

      {result && (
        <span
          className={`flex items-center gap-1 ${
            result.ok ? 'text-muted-foreground' : 'text-destructive'
          }`}
        >
          {result.ok ? <CheckCircle2 aria-hidden /> : <XCircle aria-hidden />}
          {result.status_code ? `${result.status_code}` : 'no answer'}
          {result.error && <span className="max-w-80 truncate">— {result.error}</span>}
        </span>
      )}

      {error && <span className="text-destructive">{error}</span>}
    </div>
  )
}
