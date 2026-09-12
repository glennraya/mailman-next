// Mirrors the JSON the Go API emits. Kept hand-written rather than generated
// so the shape the UI relies on is visible in one place.

export interface MailAddress {
  name?: string
  address: string
}

export interface Attachment {
  id: number
  message_id: string
  filename: string
  content_type: string
  content_id?: string
  inline: boolean
  size_bytes: number
}

export interface Message {
  id: string
  conversation_id: number
  direction: 'inbound' | 'outbound'
  message_id?: string
  in_reply_to?: string
  references?: string[]
  from?: MailAddress[]
  to?: MailAddress[]
  cc?: MailAddress[]
  bcc?: MailAddress[]
  reply_to?: MailAddress[]
  envelope: { from?: string; recipients?: string[] }
  subject: string
  sent_at: string
  text_body?: string
  html_body?: string
  size_bytes: number
  seen_at?: string
  created_at: string
  attachments?: Attachment[]
}

export interface ConversationSummary {
  id: number
  subject: string
  last_activity_at: string
  created_at: string
  message_count?: number
  unread_count?: number
  has_attachments?: boolean
  preview?: string
  last_from?: MailAddress[]
}

export interface ConversationList {
  conversations: ConversationSummary[]
  unread: number
  next_cursor?: number
}

export interface ConversationDetail {
  conversation: ConversationSummary
  messages: Message[]
}

// Delivery is one attempt to hand a reply to the app under test. Failures are
// rows too -- when a reply does not arrive, the reason has to be visible.
export interface Delivery {
  id: number
  message_id: string
  target_url: string
  format: string
  attempt: number
  status_code?: number
  error?: string
  duration_ms: number
  created_at: string
}

/** Where a reply would be delivered. Never carries the signing key. */
export interface RouteInfo {
  matched?: string
  url: string
  format: string
  fallback: boolean
  has_secret: boolean
}

export interface ReplyDraft {
  parent_id?: string
  from: string
  to: string[]
  cc?: string[]
  bcc?: string[]
  subject?: string
  text?: string
  html?: string
}

/**
 * The outcome of sending.
 *
 * `routed` means a route matched and a request was made -- not that the app
 * accepted it. The outcome is `delivery`: its status_code and error. A reply
 * is stored either way, and `reason` says why an unrouted one went nowhere.
 */
export interface ReplyResult {
  message: Message
  delivery: Delivery | null
  route: RouteInfo | null
  routed: boolean
  reason?: string
}

export interface DeliveryList {
  deliveries: Delivery[]
  route?: RouteInfo
}

export interface RouteLookup {
  routed: boolean
  route?: RouteInfo
  reason?: string
}

export interface WebhookRoute {
  url: string
  format: string
  has_secret: boolean
  verify_tls?: boolean
}

export type WebhookFormat = 'generic' | 'mailgun' | 'postmark'

export interface ServerConfig {
  version: string
  smtp_addr: string
  http_addr: string
  home: string
  max_message_bytes: number

  /**
   * What outranks the config file, keyed by document path
   * ("webhook.url" -> "MAILMAN_WEBHOOK_URL"). A field listed here cannot be
   * saved, so the settings page renders it read-only and names what holds it.
   */
  overrides: Record<string, string>

  /** False when this server cannot reload, which makes settings read-only. */
  editable: boolean

  /** Where the process actually bound, which a saved port change does not move. */
  listening: {
    http: string
    smtp: string
    needs_restart: boolean
  }

  webhook: {
    enabled: boolean
    url: string
    format: string
    has_secret: boolean
    verify_tls: boolean
    timeout_seconds: number
    timeout_ms: number
    routes: Record<string, WebhookRoute>
  }
}

/**
 * A settings save. Every field is optional and only what is present changes,
 * which is what lets the page omit a field it renders locked.
 *
 * `signing_key` is write-only and has three meanings: absent leaves the
 * stored key, `''` clears it, and a value replaces it. The key is never sent
 * back, so a page that round-tripped the response would otherwise blank it.
 *
 * `routes` replaces the whole table when present, because that is the only
 * way a removed row can be expressed -- the loader merges routes per key, so
 * omitting one would never delete it.
 */
export interface SettingsPatch {
  http_addr?: string
  smtp_addr?: string
  max_message_bytes?: number
  webhook?: {
    url?: string
    format?: string
    signing_key?: string
    timeout_seconds?: number
    verify_tls?: boolean
    routes?: Record<string, RoutePatch>
  }
}

export interface RoutePatch {
  url: string
  format?: string
  signing_key?: string
  timeout_seconds?: number
  verify_tls?: boolean
}

export interface WebhookTest {
  url: string
  format?: string
  signing_key?: string
  verify_tls?: boolean
  timeout_seconds?: number
  recipient?: string
}

export interface WebhookTestResult {
  url: string
  format: string
  status_code: number
  error: string
  ok: boolean
}

export type MailboxEventType =
  | 'hello'
  | 'message.stored'
  | 'message.deleted'
  | 'conversation.deleted'
  | 'conversation.read'
  | 'delivery.completed'
  | 'config.changed'
  | 'mailbox.cleared'

export interface MailboxEvent {
  type: MailboxEventType
  conversation_id?: number
  message_id?: string
  delivery_id?: number
  inbound?: boolean
  unread: number
  ok?: boolean
}
