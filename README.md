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
| GET | `/api/v1/attachments/{id}` | Download a part |
| GET | `/api/v1/config` | Effective settings, including webhook routes |
| GET | `/api/v1/events` | WebSocket event stream |
| GET | `/healthz` | Liveness |

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

Capture, storage, threading, the API, the live inbox and the read UI are done.
The reply loop — composing a reply, routing it by domain and posting it to your
app as an inbound webhook, with every attempt logged and retryable — is the
next milestone.
