import type { MailAddress } from './types'

export function displayName(address?: MailAddress): string {
  if (!address) return 'Unknown sender'
  return address.name?.trim() || address.address
}

export function formatAddress(address: MailAddress): string {
  return address.name?.trim() ? `${address.name} <${address.address}>` : address.address
}

export function formatAddressList(addresses?: MailAddress[]): string {
  if (!addresses?.length) return ''
  return addresses.map(formatAddress).join(', ')
}

export function initials(address?: MailAddress): string {
  const source = address?.name?.trim() || address?.address || '?'
  const parts = source.split(/[\s.@_-]+/).filter(Boolean)
  const first = parts[0]?.[0] ?? '?'
  const second = parts.length > 1 ? (parts[1]?.[0] ?? '') : ''
  return (first + second).toUpperCase()
}

/**
 * Renders a timestamp the way an inbox does: a time for today, a weekday
 * within the week, a date beyond that.
 */
export function formatWhen(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ''

  const now = new Date()
  const sameDay = date.toDateString() === now.toDateString()
  if (sameDay) {
    return date.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })
  }

  const ageDays = (now.getTime() - date.getTime()) / 86_400_000
  if (ageDays < 7 && ageDays >= 0) {
    return date.toLocaleDateString(undefined, { weekday: 'short' })
  }

  return date.toLocaleDateString(undefined, {
    month: 'short',
    day: 'numeric',
    year: date.getFullYear() === now.getFullYear() ? undefined : 'numeric',
  })
}

export function formatExactWhen(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}
