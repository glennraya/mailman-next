# Mailman

A localhost mail catcher you can **reply from**.

Point any local project at Mailman's SMTP port and every mail it sends lands in
a real inbox UI instead of a real mailbox. That much is table stakes — Mailpit
and MailHog do it too.

The difference is the other direction. Capture tools are one-way: mail goes in
and stops. But plenty of applications *expect an answer* — a support thread, a
reply to an order confirmation, an address like `order-4471@mail.myapp.test`
that ties a response back to a record. There is no good way to test that
locally. So Mailman lets you reply from the inbox and delivers that reply to
your app as a simulated inbound-email webhook, in the same shape Mailgun or
Postmark would post it. Your existing production handler receives it unchanged.

## Install

One binary. Nothing else — no PHP, no Node, no database.

**macOS, with Homebrew:**

```bash
brew install glennraya/tap/mailman
mailman service install
```

**Linux, or macOS without Homebrew:**

```bash
curl -fsSL https://raw.githubusercontent.com/glennraya/mailman-next/main/packaging/install.sh | sh
mailman service install
```

**From source:**

```bash
make install        # builds, installs to ~/.local/bin, registers the service
```

**With Go:** `go install github.com/glennraya/mailman/cmd/mailman@latest` — the
compiled UI is committed, so this needs no Node.

Every route ends with the same second step, because none of the package
managers will start a service for you. After it, Mailman is running and will
be running again after your next login.

Open <http://127.0.0.1:8383>. The first run creates `~/.mailman/` for the
database, the captured mail and an optional config file.

To run it in a terminal instead, without registering anything, just run
`mailman`.

## Run it at login

```bash
mailman service install     # register it and start it now
mailman service status      # what is registered, running and answering
mailman service stop        # stop it, leaving it registered
mailman service start       # start it again
mailman service uninstall   # stop it and remove the registration
```

| | macOS | Linux |
|---|---|---|
| Registered as | a launchd user agent | a systemd user service |
| Unit file | `~/Library/LaunchAgents/com.glennraya.mailman.plist` | `~/.config/systemd/user/mailman.service` |
| Output | `~/.mailman/mailman.log` | `journalctl --user -u mailman -f` |

It runs as you, not as root, and only while you are logged in. On a headless
Linux box where you want it up without a session, `loginctl enable-linger
$USER` — `mailman service status` says so when that applies.

Windows has no service registration yet. `mailman.exe` runs in a terminal.

If something else already holds one of Mailman's ports, the service logs what
it found and stays stopped rather than restarting every few seconds forever.
Free the port, then `mailman service start`.

`brew services` is deliberately not wired up. Mailman registers its own agent
so one command works the same on both platforms, and two managers would fight
over the same two ports — `mailman service install` refuses rather than join
a fight it would half-win.

## Point a project at it

```env
MAIL_MAILER=smtp
MAIL_HOST=127.0.0.1
MAIL_PORT=1983
MAIL_ENCRYPTION=null
```

Any credentials are accepted and nothing is ever relayed onward. Anything that
speaks SMTP works: Laravel, WordPress via an SMTP plugin, Symfony, Rails,
Django, `swaks`.

`AUTH LOGIN` is supported alongside `AUTH PLAIN`, because PHPMailer and most
WordPress SMTP plugins reach for it first.

## Reply to a message

Open a thread, click **Reply**, and send. Three things happen, in this order:

1. The reply is assembled as real RFC 5322 mail, with a `Message-ID` and the
   `In-Reply-To` and `References` headers that thread it against what it
   answers.
2. It is stored and appears in the thread, marked `sent`. This happens whether
   or not it can be delivered — a reply is never lost because a webhook is
   misconfigured.
3. Mailman looks up the route for the first envelope recipient and posts the
   reply there, waits for your app, and shows you the answer.

Which recipient decides the route is worth knowing: a reply goes to the
original's `Reply-To` when it has one, and only otherwise to its `From`. That
is the whole mechanism behind an address like `order-4471+t3h2@mail.myapp.test`
— your app puts it in `Reply-To` so an answer comes back to the record rather
than to a no-reply address. Mailgun's `recipient` and Postmark's
`OriginalRecipient` and `MailboxHash` are filled in from it.

The delivery is synchronous, so the composer reports what your app actually
returned rather than closing on a hope. A rejected delivery is not an error in
Mailman: the reply is in the thread with the status code and your app's own
response body beneath it, and **Retry** sends it again once you have fixed
whatever threw. Nothing is retried automatically — when the thing under test is
your own handler, a silent second attempt is noise.

A reply to an address no route covers is stored and says so, in the composer
before you send and in the thread afterwards. It is never reported as
delivered.

Replies cannot carry attachments yet.

## How it works

One binary, one process, two listeners over one SQLite file:

```
127.0.0.1:1983    SMTP capture
127.0.0.1:8383    inbox UI, JSON API and event stream
~/.mailman/       mailman.db, mail/, config.json
```

Move either with `-smtp` / `-http`, or with `MAILMAN_SMTP_ADDR` /
`MAILMAN_HTTP_ADDR`. `-home` (or `MAILMAN_HOME`) relocates the data directory.

Mailman deliberately avoids 1025 and 8025, so it can sit alongside a Mailpit
or MailHog you already have running rather than fighting it for the port. If
something is already serving one of Mailman's ports, startup fails with a
message naming what it found — it never starts half-working. Running as a
service, that same condition is a clean stop rather than a failure, so the
login manager leaves it down instead of retrying forever.

The inbox updates over a WebSocket, so captured mail appears the moment it
arrives rather than on a poll.

## Features

- **The reply loop** — write a reply in the inbox and Mailman delivers it to
  your app as an inbound-email webhook, routed by the recipient's domain, in
  Mailgun's, Postmark's or Mailman's own shape. Every attempt is recorded with
  the status code, the duration and whatever your app said back, and any of
  them can be sent again.
- **Threading** — replies group by `In-Reply-To` and `References`, with a
  reply-prefix subject fallback. Two unrelated mails that merely share a
  subject stay apart, which is what keeps transactional mail readable.
- **Bcc recovery** — a recipient in the SMTP envelope but in no header was
  blind-copied, and Mailman shows it. This can only be captured at receive
  time.
- **Safe HTML** — bodies render in an iframe with no `allow-scripts` and a
  strict `Content-Security-Policy`. Remote images load so a template looks the
  way its recipients will see it; one click per message blocks them again when
  a tracking pixel should not learn that you opened it. `cid:` images resolve
  to their inline parts.
- **Forgiving parsing** — malformed mail is stored and shown rather than
  dropped, and the verbatim `.eml` is always one click away.
- **Search** across subjects, bodies and addresses.

## Configuration

Everything has a working default. `~/.mailman/config.json` is only needed for
webhook routing:

```json
{
  "webhook": {
    "url": "http://myapp.test/webhooks/inbound",
    "format": "generic",
    "timeout_seconds": 5,
    "verify_tls": true,
    "routes": {
      "mail.myapp.test": {
        "url": "https://api.myapp.test/webhooks/mailgun/inbound",
        "format": "mailgun",
        "signing_key": "key-..."
      },
      "*.otherapp.test": { "url": "http://otherapp.test/inbound" }
    }
  }
}
```

Routes are matched against the domain of the reply's first envelope recipient.
An exact key wins; otherwise the longest matching `*.` pattern does, and
`*.example.test` matches both subdomains and the bare domain. A route inherits
`format`, `signing_key`, `timeout_seconds` and `verify_tls` from the top level,
so a project speaking the default format needs only a URL. A domain with no
route falls back to `webhook.url`; with no fallback, replies to it are not
forwarded, and the UI says so rather than claiming success.

Scalars can also be set with `MAILMAN_WEBHOOK_URL`, `MAILMAN_WEBHOOK_FORMAT`,
`MAILMAN_WEBHOOK_SIGNING_KEY`, `MAILMAN_WEBHOOK_TIMEOUT` and
`MAILMAN_WEBHOOK_VERIFY_TLS`, which take precedence over the file.

### What each format posts

| `format` | Body | Authenticated with |
|---|---|---|
| `generic` | `application/json` — the parsed message, its headers in order, and the verbatim `.eml` under `raw` | `X-Mailman-Signature: sha256=<hex>`, an HMAC over the exact request body |
| `mailgun` | `multipart/form-data` — `recipient`, `sender`, `from`, `subject`, `body-plain`, `body-html`, `stripped-text`, `stripped-signature`, `message-headers`, `domain`, `attachment-count`, plus **every MIME header as its own field** (`Message-Id`, `In-Reply-To`, `References`, …) and `X-Mailgun-Incoming: Yes` | `signature`, `timestamp` and `token` **in the body**, where `signature` is `HMAC-SHA256(key, timestamp + token)` — what Mailgun's own verification helpers recompute |
| `postmark` | `application/json` — `From`/`FromFull`, `To`/`ToFull`, `Cc`, `Bcc`, `OriginalRecipient`, `MailboxHash`, `Subject`, `MessageID`, `TextBody`, `HtmlBody`, `StrippedTextReply`, `Headers` | HTTP Basic, the way Postmark's inbound URLs are secured — Postmark signs nothing, so a `signing_key` on a Postmark route is sent as the Basic user |

The per-header fields are not redundancy: handlers read them directly, and a
gate as ordinary as `isset($data['In-Reply-To'])` drops a reply that arrives
without them. `message-headers` is the complete, ordered record; the individual
fields are what code actually indexes.

Two details of the Postmark shape are faithful rather than convenient, because
a production handler depends on both: `MessageID` is a Postmark-style UUID, not
the mail's `Message-ID` — that travels in `Headers`, where Postmark puts it —
and `Headers` omits the fields promoted to the top level.

Set `signing_key` to whatever your app already verifies against and its
existing handler works unchanged. With no key configured, Mailgun's `signature`
field is omitted rather than computed from an empty one: a verifying handler
then fails for an obvious reason instead of a mysterious one.

Two limits worth knowing. A blank `signing_key` on a route inherits the
top-level one, so there is no way to say "this one route is unsigned" while a
key exists above it. And config is read at startup, so editing `config.json`
takes a restart — the reply keeps in the database until then, and **Retry** is
still there to click.

## JSON API

No authentication — it is a localhost tool. Useful for CI assertions.

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/v1/conversations?q=&limit=&before=` | List and search threads |
| GET | `/api/v1/conversations/{id}` | One thread with every message |
| DELETE | `/api/v1/conversations/{id}` | Delete a thread |
| POST | `/api/v1/conversations/{id}/seen` | Mark a thread read |
| GET | `/api/v1/messages/{id}` | One message |
| GET | `/api/v1/messages/{id}/raw` | Verbatim `.eml` |
| GET | `/api/v1/messages/{id}/html` | Sandboxed body (`?images=0` to block remote images) |
| POST | `/api/v1/messages/{id}/seen` | Mark one message read |
| DELETE | `/api/v1/messages/{id}` | Delete one message |
| POST | `/api/v1/messages` | Inject mail (`{raw}` or `{from,to[],subject,text,html}`) |
| DELETE | `/api/v1/messages` | Empty the mailbox (CI reset) |
| POST | `/api/v1/replies` | Send a reply and deliver it (`{parent_id,from,to[],cc[],bcc[],subject,text,html}`) |
| GET | `/api/v1/messages/{id}/deliveries` | Every webhook attempt for one reply |
| POST | `/api/v1/messages/{id}/deliveries` | Send it again |
| GET | `/api/v1/webhook/route?recipient=` | Where a reply to an address would go |
| GET | `/api/v1/attachments/{id}` | Download a part |
| GET | `/api/v1/config` | Effective settings, including webhook routes |
| GET | `/api/v1/events` | WebSocket event stream |
| GET | `/healthz` | Liveness |

`POST /api/v1/replies` answers `201` with the stored message, the delivery
attempt and the route it followed. It answers `201` even when your app refused
the reply: the message was still written, and `delivery.status_code` and
`delivery.error` are the outcome. `routed` is false only when no route matched
at all, and `reason` then says so — so a CI assertion can tell "my handler
threw" from "Mailman had nowhere to send it".

Writes are protected against cross-origin requests, since Mailman listens on
loopback while you browse the rest of the web. Safe methods are unaffected.

## Working on Mailman

```bash
make install-ui     # npm install for the frontend
make test           # Go suite plus the frontend type check
make build          # UI + binary into build/mailman
make install        # build, install to ~/.local/bin, register the service
make dist           # cross-compile all five platforms
make checksums      # what the install script verifies against
```

For frontend work, run the server and the Vite dev server side by side:

```bash
make dev            # Go server on :8383
make dev-ui         # Vite on :5173, proxying the API and the WebSocket
```

`web/dist` is committed so `go build ./...` and `go install` work without
Node. Rebuild it with `make ui` and commit the result whenever you change
anything under `web/src` — CI fails if it is stale.

## Status

The loop is closed. Capture, storage, threading, the API, the live inbox, the
read UI, and replying — routed by domain, posted to your app as an inbound
webhook in any of the three shapes, with every attempt logged and retryable —
all work.

Still to come: attachments on replies, which the message builder already
handles but the composer has no file picker for; automatic retry with backoff;
and picking up a changed `config.json` without a restart.
