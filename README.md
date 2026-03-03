# Hello Web Push PWA (Go + VPS)

Minimal Web Push PWA with a Go backend using `net/http` and `github.com/SherClockHolmes/webpush-go`.

## Features

- Same-origin API + static hosting from one server.
- PWA frontend (`public/`) with:
  - `Enable Notifications`
  - `Send Hello World`
- Push subscription by stable `deviceId` (stored in browser `localStorage`).
- Admin-protected send endpoint (`X-Admin-Token`).
- Subscription storage modes:
  - `memory` (default)
  - `json`
  - `sqlite`
- Automatic removal of invalid subscriptions when push providers return `404` / `410`.

## Project structure

```text
hello-webpush-pwa/
├── cmd/
│   └── vapidpub/
│       └── main.go
├── public/
│   ├── app.js
│   ├── icon.svg
│   ├── index.html
│   ├── manifest.json
│   └── sw.js
├── go.mod
├── main.go
└── README.md
```

## Requirements

- Go (latest stable recommended)
- HTTPS in production (required for reliable push, especially iOS Safari)

## Environment variables

- `PORT` (default: `3000`)
- `VAPID_SUBJECT` (example: `mailto:you@example.com`)
- `VAPID_PUBLIC_B64` (base64url uncompressed public key)
- `VAPID_PRIVATE_PEM_PATH` (path to EC P-256 private key PEM)
- `ADMIN_TOKEN` (**required**, server exits if missing)
- `STORAGE_MODE` (optional: `memory` | `json` | `sqlite`)
- `STORAGE_PATH` (optional file path for `json` or `sqlite`)

## Generate VAPID keys with OpenSSL

Generate P-256 private key PEM:

```bash
openssl ecparam -name prime256v1 -genkey -noout -out vapid_private.pem
```

(Optionally) output public PEM:

```bash
openssl ec -in vapid_private.pem -pubout -out vapid_public.pem
```

Get `VAPID_PUBLIC_B64` (and derived private scalar for verification) using helper:

```bash
go run ./cmd/vapidpub -pem ./vapid_private.pem
```

Use the printed `VAPID_PUBLIC_B64` as env var. The backend reads private key directly from `VAPID_PRIVATE_PEM_PATH`.

## Run locally

```bash
go mod tidy

export PORT=3000
export VAPID_SUBJECT='mailto:you@example.com'
export VAPID_PUBLIC_B64='PASTE_PUBLIC_KEY_FROM_HELPER'
export VAPID_PRIVATE_PEM_PATH='./vapid_private.pem'
export ADMIN_TOKEN='change-me'
# optional:
# export STORAGE_MODE='json'
# export STORAGE_PATH='./data/subscriptions.json'

go run .
```

Open: `http://localhost:3000`

## API

### `GET /vapidPublicKey`

Returns:

```json
{ "key": "<VAPID_PUBLIC_B64>" }
```

### `POST /subscribe`

Body:

```json
{
  "deviceId": "<stable-device-id>",
  "subscription": {
    "endpoint": "...",
    "expirationTime": null,
    "keys": {
      "p256dh": "...",
      "auth": "..."
    }
  }
}
```

### `POST /sendHello`

Headers:

- `Content-Type: application/json`
- `X-Admin-Token: <ADMIN_TOKEN>`

Body:

```json
{}
```

Sends this payload to all subscriptions:

```json
{
  "title": "Hello 👋",
  "body": "Hello World push from your Go VPS backend!",
  "url": "/"
}
```

TTL is set to `60` seconds.

### `POST /sendNotification`

Headers:

- `Content-Type: application/json`
- `X-Admin-Token: <ADMIN_TOKEN>`

Body:

```json
{
  "title": "New push dynamic title",
  "body": "New dynamic body",
  "url": "/new-dynamic-url"
}
```

## Curl example for `/sendHello`

```bash
curl -i -X POST 'https://your-domain.example/sendHello' \
  -H 'Content-Type: application/json' \
  -H 'X-Admin-Token: change-me' \
  -d '{}'
```

## CLI command for dynamic push content

Build once:

```bash
go build -o pushnotify ./cmd/pushnotify
```

Run:

```bash
./pushnotify \
  --title="New push dynamic title" \
  --body="New dynamic body" \
  --url="www.google.com" \
  --admin-token="change-me"
```

Optional flags:

- `--endpoint` (default: `https://pushnotification.newsorbit.tech/sendNotification`)
- `--admin-token` can be omitted if `ADMIN_TOKEN` env var is set
- `--url` supports relative paths (`/offers`) and full/bare domains (`https://example.com` or `www.example.com`)

## Deploy on VPS with systemd

1. Build binary and place project on server.
2. Put env vars in `/etc/hello-webpush.env`.
3. Use this service file:

```ini
# /etc/systemd/system/hello-webpush.service
[Unit]
Description=Hello Web Push PWA
After=network.target

[Service]
Type=simple
User=www-data
Group=www-data
WorkingDirectory=/opt/hello-webpush-pwa
EnvironmentFile=/etc/hello-webpush.env
ExecStart=/opt/hello-webpush-pwa/hello-webpush
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
```

Example `/etc/hello-webpush.env`:

```bash
PORT=3000
VAPID_SUBJECT=mailto:you@example.com
VAPID_PUBLIC_B64=PASTE_PUBLIC_KEY
VAPID_PRIVATE_PEM_PATH=/opt/hello-webpush-pwa/keys/vapid_private.pem
ADMIN_TOKEN=change-me
STORAGE_MODE=sqlite
STORAGE_PATH=/opt/hello-webpush-pwa/data/subscriptions.db
```

Enable service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now hello-webpush
sudo systemctl status hello-webpush
```

## Caddy reverse proxy + Let’s Encrypt (HTTPS)

Use DNS-resolving domain pointed to VPS IP.

```caddyfile
# /etc/caddy/Caddyfile
your-domain.example {
  encode gzip
  reverse_proxy 127.0.0.1:3000
}
```

Reload Caddy:

```bash
sudo systemctl reload caddy
```

Caddy will automatically provision/renew Let’s Encrypt certificates.

Important: Web Push on iPhone Safari is reliable only with:

- HTTPS origin
- Installed PWA (Add to Home Screen)

## iPhone steps

1. Open your HTTPS site in Safari.
2. Share -> Add to Home Screen.
3. Open the installed app from Home Screen.
4. Tap `Enable Notifications` and allow permission.

## Quick Test Checklist

- Open site on each device -> Enable Notifications
- Send Hello World -> verify notification appears on all devices
- iPhone: confirm it only works when installed PWA + HTTPS
