# MX Sentinel Web Dashboard

Minimal Next.js dashboard for MX Sentinel.

## Setup

```bash
cd web
npm install
cp .env.example .env.local
```

Edit `.env.local` and set the API base URL:

```
NEXT_PUBLIC_API_BASE=http://localhost:8080
```

`NEXT_PUBLIC_API_TOKEN` is supported as a **dev fallback** — if it is set and
no session token exists in `localStorage`, it will be used for all requests.
For production use, leave it unset and authenticate via the login page instead.

## Authentication

The dashboard uses user session tokens issued by `apid`. Create a user with:

```bash
mxctl user create --email you@example.com --password secret --tenant demo
```

Then open http://localhost:3000/login, enter your email and password, and the
dashboard will store the session token in `localStorage` under the key
`mxs_token`. All subsequent API calls use that token as a Bearer credential.

To log out, click **Log out** in the top navigation bar. The token is cleared
and you are redirected back to `/login`.

### Dev fallback (API token)

If you prefer to skip the login flow during local development, set a static API
key in `.env.local`:

```
NEXT_PUBLIC_API_TOKEN=<your-token>
```

Generate a key with:

```bash
mxctl apikey create --tenant demo
```

When `NEXT_PUBLIC_API_TOKEN` is set and no `mxs_token` exists in
`localStorage`, the env-var token is used automatically.

## Run

```bash
npm run dev
```

The dashboard runs on http://localhost:3000 and talks to `apid` on `:8080`.

## Build

```bash
npm run build
npm start
```
