import { useCallback, useEffect, useState } from 'react'
import { Loader2, Plus, Save, Trash2 } from 'lucide-react'

import { api } from '@/api'
import { SettingsField } from '@/components/SettingsField'
import { WebhookTester } from '@/components/WebhookTester'
import { Alert, AlertDescription } from '@/components/ui/alert'
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
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { ServerConfig, SettingsPatch, WebhookFormat } from '@/types'

interface Props {
  open: boolean
  onClose: () => void
  config: ServerConfig | null
  /** Bumped on a config.changed event, so another tab's save is picked up. */
  revision: number
  onSaved: (config: ServerConfig) => void
}

const FORMATS: WebhookFormat[] = ['generic', 'mailgun', 'postmark']

/** One editable row of the routes table. */
interface RouteRow {
  /** A stable key, so renaming a domain does not remount the row. */
  key: number
  domain: string
  url: string
  format: string
  /** Undefined leaves the stored key; '' clears it; a value replaces it. */
  signingKey?: string
  hasSecret: boolean
}

/**
 * Where a developer points Mailman at their app.
 *
 * Webhook changes take effect the moment they are saved -- the reason this
 * page exists is that editing config.json and restarting was the last thing
 * standing between installing Mailman and receiving a reply. Ports are saved
 * too but cannot move a listener that is already bound, so the page says so
 * rather than implying otherwise.
 */
export function SettingsModal({ open, onClose, config, revision, onSaved }: Props) {
  const [url, setUrl] = useState('')
  const [format, setFormat] = useState<string>('generic')
  const [signingKey, setSigningKey] = useState<string | undefined>(undefined)
  const [verifyTLS, setVerifyTLS] = useState(true)
  const [timeout, setTimeoutSeconds] = useState('5')
  const [httpAddr, setHttpAddr] = useState('')
  const [smtpAddr, setSmtpAddr] = useState('')
  const [routes, setRoutes] = useState<RouteRow[]>([])
  const [nextKey, setNextKey] = useState(0)

  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [dirty, setDirty] = useState(false)

  // Load the form from the server's view of the settings. Only when the
  // dialog is closed or nothing has been typed, so a save from another tab
  // cannot discard a half-built routes table.
  const reset = useCallback((from: ServerConfig) => {
    setUrl(from.webhook.url)
    setFormat(from.webhook.format)
    setSigningKey(undefined)
    setVerifyTLS(from.webhook.verify_tls)
    setTimeoutSeconds(String(from.webhook.timeout_seconds))
    setHttpAddr(from.http_addr)
    setSmtpAddr(from.smtp_addr)

    // Sorted, because Go randomizes map order and the rows would otherwise
    // jump around on every save.
    const rows = Object.entries(from.webhook.routes)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([domain, route], index) => ({
        key: index,
        domain,
        url: route.url,
        format: route.format,
        hasSecret: route.has_secret,
      }))

    setRoutes(rows)
    setNextKey(rows.length)
    setDirty(false)
    setError(null)
  }, [])

  useEffect(() => {
    if (!config) return
    if (dirty && open) return
    reset(config)
    // dirty is deliberately not a dependency: this should re-run when the
    // server's settings change, not when the draft does.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [config, open, revision, reset])

  const locked = (field: string) => config?.overrides?.[field]
  const readOnly = config ? !config.editable : true

  const change = <T,>(setter: (value: T) => void) => (value: T) => {
    setter(value)
    setDirty(true)
  }

  const updateRoute = (key: number, patch: Partial<RouteRow>) => {
    setRoutes((current) => current.map((row) => (row.key === key ? { ...row, ...patch } : row)))
    setDirty(true)
  }

  const save = async () => {
    setSaving(true)
    setError(null)

    // Only what this page is allowed to change is sent. A locked field is
    // omitted rather than submitted and refused.
    const webhook: NonNullable<SettingsPatch['webhook']> = {}
    if (!locked('webhook.url')) webhook.url = url.trim()
    if (!locked('webhook.format')) webhook.format = format
    if (!locked('webhook.verify_tls')) webhook.verify_tls = verifyTLS
    if (!locked('webhook.signing_key') && signingKey !== undefined) {
      webhook.signing_key = signingKey
    }
    if (!locked('webhook.timeout_seconds')) {
      const seconds = Number(timeout)
      if (!Number.isFinite(seconds) || seconds <= 0) {
        setError('The timeout must be a positive number of seconds.')
        setSaving(false)
        return
      }
      webhook.timeout_seconds = seconds
    }

    webhook.routes = Object.fromEntries(
      routes
        .filter((row) => row.domain.trim() && row.url.trim())
        .map((row) => [
          row.domain.trim().toLowerCase(),
          {
            url: row.url.trim(),
            format: row.format || undefined,
            signing_key: row.signingKey,
          },
        ]),
    )

    const patch: SettingsPatch = { webhook }
    if (!locked('http_addr')) patch.http_addr = httpAddr.trim()
    if (!locked('smtp_addr')) patch.smtp_addr = smtpAddr.trim()

    try {
      const saved = await api.saveConfig(patch)
      onSaved(saved)
      reset(saved)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not save')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
    >
      <DialogContent className="max-h-[85vh] gap-0 sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>Settings</DialogTitle>
          <DialogDescription>
            Where Mailman delivers a reply. Webhook changes apply immediately.
          </DialogDescription>
        </DialogHeader>

        {readOnly && (
          <Alert className="mt-4">
            <AlertDescription>
              This server cannot reload its configuration, so settings are read-only here. Edit{' '}
              <code className="rounded bg-muted px-1 py-0.5">{config?.home}/config.json</code>{' '}
              instead.
            </AlertDescription>
          </Alert>
        )}

        <Tabs defaultValue="delivery" className="mt-4 min-h-0">
          <TabsList variant="line">
            <TabsTrigger value="delivery">Delivery</TabsTrigger>
            <TabsTrigger value="routes">
              Routes{routes.length > 0 && ` (${routes.length})`}
            </TabsTrigger>
            <TabsTrigger value="server">Server</TabsTrigger>
          </TabsList>

          <ScrollArea className="max-h-[52vh]">
            <TabsContent value="delivery" className="space-y-4 px-1 py-4">
              <SettingsField
                label="Webhook URL"
                lockedBy={locked('webhook.url')}
                hint="Where a reply is posted when no route matches its recipient. Leave it empty to forward nothing by default."
              >
                {(id, disabled) => (
                  <Input
                    id={id}
                    value={url}
                    disabled={disabled || readOnly}
                    placeholder="http://myapp.test/webhooks/inbound"
                    onChange={(event) => change(setUrl)(event.target.value)}
                  />
                )}
              </SettingsField>

              <SettingsField
                label="Format"
                lockedBy={locked('webhook.format')}
                hint="mailgun and postmark reproduce what those providers post, so an app's existing handler works unchanged. generic is Mailman's own shape."
              >
                {(id, disabled) => (
                  <Select
                    value={format}
                    disabled={disabled || readOnly}
                    onValueChange={change(setFormat)}
                  >
                    <SelectTrigger id={id} className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {FORMATS.map((name) => (
                        <SelectItem key={name} value={name}>
                          {name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </SettingsField>

              <SettingsField
                label="Signing key"
                lockedBy={locked('webhook.signing_key')}
                hint={
                  config?.webhook.has_secret
                    ? 'A key is set. Type to replace it, or clear the field and save to remove it.'
                    : 'Set this to whatever your app verifies against — for a Mailgun handler, the same value as its MAILGUN_SECRET.'
                }
              >
                {(id, disabled) => (
                  <Input
                    id={id}
                    type="password"
                    value={signingKey ?? ''}
                    disabled={disabled || readOnly}
                    placeholder={config?.webhook.has_secret ? '••••••••' : 'unsigned'}
                    onChange={(event) => change(setSigningKey)(event.target.value)}
                  />
                )}
              </SettingsField>

              <div className="flex items-center gap-6">
                <SettingsField label="Timeout (seconds)" lockedBy={locked('webhook.timeout_seconds')}>
                  {(id, disabled) => (
                    <Input
                      id={id}
                      value={timeout}
                      disabled={disabled || readOnly}
                      className="w-24"
                      onChange={(event) => change(setTimeoutSeconds)(event.target.value)}
                    />
                  )}
                </SettingsField>

                <SettingsField
                  label="Verify TLS"
                  lockedBy={locked('webhook.verify_tls')}
                  hint="Turn off for an app behind a self-signed certificate."
                >
                  {(id, disabled) => (
                    <Switch
                      id={id}
                      checked={verifyTLS}
                      disabled={disabled || readOnly}
                      onCheckedChange={change(setVerifyTLS)}
                    />
                  )}
                </SettingsField>
              </div>

              <Separator />

              <WebhookTester
                disabled={!url.trim()}
                target={() => ({
                  url: url.trim(),
                  format,
                  signing_key: signingKey,
                  verify_tls: verifyTLS,
                })}
              />
            </TabsContent>

            <TabsContent value="routes" className="space-y-3 px-1 py-4">
              <p className="text-xs text-muted-foreground">
                A reply is routed by the domain of its recipient — which is the original's{' '}
                <code className="rounded bg-muted px-1 py-0.5">Reply-To</code> when it has one. An
                exact domain wins; otherwise the longest{' '}
                <code className="rounded bg-muted px-1 py-0.5">*.</code> pattern does.
              </p>

              {routes.map((row) => (
                <div key={row.key} className="space-y-2 rounded-lg border p-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <Input
                      value={row.domain}
                      disabled={readOnly}
                      placeholder="mail.myapp.test"
                      className="w-56"
                      onChange={(event) => updateRoute(row.key, { domain: event.target.value })}
                    />
                    <Input
                      value={row.url}
                      disabled={readOnly}
                      placeholder="http://myapp.test/webhooks/mailgun/inbound"
                      className="min-w-0 flex-1"
                      onChange={(event) => updateRoute(row.key, { url: event.target.value })}
                    />
                    <Select
                      value={row.format || format}
                      disabled={readOnly}
                      onValueChange={(value) => updateRoute(row.key, { format: value })}
                    >
                      <SelectTrigger className="w-32">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {FORMATS.map((name) => (
                          <SelectItem key={name} value={name}>
                            {name}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label={`Remove the route for ${row.domain || 'this domain'}`}
                      disabled={readOnly}
                      className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                      onClick={() => {
                        setRoutes((current) => current.filter((r) => r.key !== row.key))
                        setDirty(true)
                      }}
                    >
                      <Trash2 aria-hidden />
                    </Button>
                  </div>

                  <div className="flex flex-wrap items-center gap-3">
                    <Label className="text-xs text-muted-foreground">Signing key</Label>
                    <Input
                      type="password"
                      value={row.signingKey ?? ''}
                      disabled={readOnly}
                      placeholder={row.hasSecret ? '••••••••' : 'inherits the default'}
                      className="w-48"
                      onChange={(event) =>
                        updateRoute(row.key, { signingKey: event.target.value })
                      }
                    />
                    <WebhookTester
                      disabled={!row.url.trim()}
                      target={() => ({
                        url: row.url.trim(),
                        format: row.format || format,
                        signing_key: row.signingKey,
                        recipient: `order-1234+test@${row.domain.replace(/^\*\./, 'probe.')}`,
                      })}
                    />
                  </div>

                  {/* The key is never sent to the browser, so a rename cannot
                      carry it across. Say so rather than lose it quietly. */}
                  {row.hasSecret && row.domain !== initialDomain(config, row.key) && (
                    <p className="text-xs text-foreground">
                      Renaming a route loses its signing key — re-enter it before saving.
                    </p>
                  )}
                </div>
              ))}

              <Button
                variant="outline"
                size="sm"
                disabled={readOnly}
                onClick={() => {
                  setRoutes((current) => [
                    ...current,
                    { key: nextKey, domain: '', url: '', format: '', hasSecret: false },
                  ])
                  setNextKey((key) => key + 1)
                  setDirty(true)
                }}
              >
                <Plus aria-hidden />
                Add a route
              </Button>
            </TabsContent>

            <TabsContent value="server" className="space-y-4 px-1 py-4">
              <Alert>
                <AlertDescription>
                  A port change is saved but cannot move a listener that is already bound. Restart
                  Mailman to pick it up.
                </AlertDescription>
              </Alert>

              <SettingsField
                label="Inbox and API"
                lockedBy={locked('http_addr')}
                hint={`Currently serving ${config?.listening.http ?? '…'}`}
              >
                {(id, disabled) => (
                  <Input
                    id={id}
                    value={httpAddr}
                    disabled={disabled || readOnly}
                    placeholder="127.0.0.1:8383"
                    onChange={(event) => change(setHttpAddr)(event.target.value)}
                  />
                )}
              </SettingsField>

              <SettingsField
                label="SMTP capture"
                lockedBy={locked('smtp_addr')}
                hint={`Currently listening on ${config?.listening.smtp ?? '…'}. Point your project's MAIL_PORT at this.`}
              >
                {(id, disabled) => (
                  <Input
                    id={id}
                    value={smtpAddr}
                    disabled={disabled || readOnly}
                    placeholder="127.0.0.1:1983"
                    onChange={(event) => change(setSmtpAddr)(event.target.value)}
                  />
                )}
              </SettingsField>

              <Separator />

              <dl className="space-y-1 text-xs text-muted-foreground">
                <Row label="Version" value={config?.version ?? '—'} />
                <Row label="Data" value={config?.home ?? '—'} />
                <Row label="Config" value={config ? `${config.home}/config.json` : '—'} />
              </dl>
            </TabsContent>
          </ScrollArea>
        </Tabs>

        {error && (
          <Alert variant="destructive" className="mt-2">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}

        <DialogFooter className="mt-4 sm:items-center sm:justify-between">
          <p className="text-xs text-muted-foreground">
            {config?.listening.needs_restart
              ? 'A saved change is waiting on a restart.'
              : dirty
                ? 'Unsaved changes.'
                : ''}
          </p>
          <div className="flex items-center gap-2">
            <DialogClose asChild>
              <Button variant="ghost" disabled={saving}>
                Close
              </Button>
            </DialogClose>
            <Button disabled={readOnly || saving || !dirty} onClick={() => void save()}>
              {saving ? <Loader2 className="animate-spin" aria-hidden /> : <Save aria-hidden />}
              {saving ? 'Saving' : 'Save'}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * The domain a row was loaded with, used only to notice a rename. Rows are
 * keyed by their load order, so this is that entry of the sorted table.
 */
function initialDomain(config: ServerConfig | null, key: number): string | undefined {
  if (!config) return undefined

  return Object.keys(config.webhook.routes).sort((a, b) => a.localeCompare(b))[key]
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-2">
      <dt className="w-16 shrink-0">{label}</dt>
      <dd className="min-w-0 flex-1 truncate font-mono">{value}</dd>
    </div>
  )
}
