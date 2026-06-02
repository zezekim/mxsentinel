# MX Sentinel Web Dashboard

Minimal Next.js dashboard for MX Sentinel.

## Setup

```bash
cd web
npm install
cp .env.example .env.local
```

Edit `.env.local` and set your API token:

```
NEXT_PUBLIC_API_BASE=http://localhost:8080
NEXT_PUBLIC_API_TOKEN=<your-token>
```

Generate a token with:

```bash
mxctl apikey create --tenant demo
```

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
