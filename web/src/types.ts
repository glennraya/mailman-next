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
}

export interface ServerConfig {
  version: string
  smtp_addr: string
  http_addr: string
  home: string
  max_message_bytes: number
  webhook: {
    enabled: boolean
    url: string
    format: string
    verify_tls: boolean
    timeout_ms: number
    routes: Record<string, WebhookRoute>
  }
}

export type MailboxEventType =
  | 'hello'
  | 'message.stored'
  | 'message.deleted'
  | 'conversation.deleted'
  | 'conversation.read'
  | 'delivery.completed'
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
