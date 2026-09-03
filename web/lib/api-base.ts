// Base URL the browser uses for /v1/... calls.
//
// "same-origin" (what deploy/docker-compose.prod.yml bakes in) collapses to "", so every
// request goes back to whichever hostname served the page. That is what lets a single
// build serve every domain Caddy fronts — see MXS_EXTRA_DOMAINS in deploy/Caddyfile. An
// absolute URL here would pin all traffic to one host regardless of how the user arrived.
// Local dev, with nothing set, falls back to apid's port.
const configured = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

export const API_BASE = configured === "same-origin" ? "" : configured;
