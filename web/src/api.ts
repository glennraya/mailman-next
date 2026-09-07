import type { ConversationDetail, ConversationList, Message, ServerConfig } from '@/types'

// In development the Vite proxy forwards these to the Go server; in the
// built binary the SPA and the API share an origin, so a relative path is
// correct in both.
const BASE = '/api/v1'

class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(BASE + path, {
    ...init,
    headers: { Accept: 'application/json', ...init?.headers },
  })

  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`
    try {
      const body = (await response.json()) as { error?: string }
      if (body.error) message = body.error
    } catch {
      // A non-JSON error body is not worth surfacing over the status line.
    }
    throw new ApiError(message, response.status)
  }

  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

export const api = {
  config: () => request<ServerConfig>('/config'),

  conversations: (query = '', cursor?: number) => {
    const params = new URLSearchParams()
    if (query.trim()) params.set('q', query.trim())
    if (cursor) params.set('before', String(cursor))
    const suffix = params.toString()
    return request<ConversationList>(`/conversations${suffix ? `?${suffix}` : ''}`)
  },

  conversation: (id: number) => request<ConversationDetail>(`/conversations/${id}`),

  deleteConversation: (id: number) =>
    request<{ deleted: boolean }>(`/conversations/${id}`, { method: 'DELETE' }),

  markConversationSeen: (id: number) =>
    request<{ unread: number }>(`/conversations/${id}/seen`, { method: 'POST' }),

  message: (id: string) => request<Message>(`/messages/${id}`),

  deleteMessage: (id: string) =>
    request<{ deleted: boolean; conversation_deleted: boolean; conversation_id: number }>(
      `/messages/${id}`,
      { method: 'DELETE' },
    ),

  clearMailbox: () => request<{ deleted: number }>('/messages', { method: 'DELETE' }),

  // Served as documents and files rather than JSON, so these are URLs the
  // browser fetches directly.
  htmlUrl: (id: string, blockRemoteImages = false) =>
    `${BASE}/messages/${id}/html${blockRemoteImages ? '?images=0' : ''}`,
  rawUrl: (id: string) => `${BASE}/messages/${id}/raw`,
  attachmentUrl: (id: number) => `${BASE}/attachments/${id}`,
}

export { ApiError }
