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
