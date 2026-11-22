# Deploying

The `Deploy (Lightsail)` workflow ([`.github/workflows/deploy.yml`](../.github/workflows/deploy.yml))
runs [`deploy/lightsail/deploy.sh`](../deploy/lightsail/deploy.sh) on a
Lightsail instance over SSH. The script runs three containers on one Docker
network:

```
browser ──HTTPS──▶ [Cloudflare] ──HTTPS──▶ Caddy :443 ──HTTP──▶ app :8080 ──▶ Postgres
```

Caddy ([`deploy/lightsail/Caddyfile`](../deploy/lightsail/Caddyfile))
terminates TLS and proxies to the app. The app trusts the `X-Real-IP` and
`X-Forwarded-Proto` headers only from the Docker network's subnet
(`EXPENSE_TRUSTED_PROXIES`, set by the script), so it sees the real client
address (failed-login throttling is per username and address) and marks the
session cookie `Secure`.

## Settings

Repository variables (or secrets) used by the workflow:

| Setting | Required | Meaning |
|---|---|---|
| `LIGHTSAIL_IP_ADDR` | yes | Public IP of the instance (SSH target) |
| `LIGHTSAIL_SSH_KEY_PAIR` | yes (secret) | Private key (.pem) of the instance's key pair |
| `LIGHTSAIL_USER` | no | SSH user, default `ec2-user` |
| `LIGHTSAIL_DOMAIN` | no | Domain to serve instead of the bare IP |
| `LIGHTSAIL_CLOUDFLARE` | no | `true` when `LIGHTSAIL_DOMAIN` is proxied by Cloudflare |
| `CLOUDFLARE_ORIGIN_CERT` | no (secret) | Cloudflare Origin CA certificate (PEM) |
| `CLOUDFLARE_ORIGIN_KEY` | no (secret) | Its private key (PEM) |

## Behind the Cloudflare proxy

To serve the domain through Cloudflare (DNS record with the orange cloud,
"Proxied"):

1. **DNS**: in Cloudflare, add an `A` record for the domain pointing at the
   instance's public IP, Proxy status **Proxied**. Set `LIGHTSAIL_DOMAIN` to
   that name.
2. **Origin certificate** (recommended): in Cloudflare, SSL/TLS → Origin
   Server → Create Certificate (RSA or ECDSA, hostnames covering the domain).
   Store the certificate as the `CLOUDFLARE_ORIGIN_CERT` secret and the
   private key as `CLOUDFLARE_ORIGIN_KEY`. Caddy then serves it instead of
   asking Let's Encrypt.
3. **SSL/TLS mode**: set SSL/TLS → Overview to **Full (strict)**. Never
   **Flexible**: Cloudflare would talk plain HTTP to the instance, Caddy
   redirects HTTP to HTTPS, and browsers loop.
4. Set the repository variable `LIGHTSAIL_CLOUDFLARE` to `true` and run the
   workflow (or push to `main`).

With `LIGHTSAIL_CLOUDFLARE=true` the script fetches Cloudflare's IP ranges
(<https://www.cloudflare.com/ips/>, with a built-in copy as fallback) on each
deploy and configures Caddy to:

- take the visitor's address from `CF-Connecting-IP`, but only on
  connections from those ranges (so nobody can fake it), and pass it to the
  app as `X-Real-IP`;
- drop connections to the site that do not come from Cloudflare (or from the
  instance itself, for the deploy's smoke test), so the WAF and rate limits
  in front of it cannot be bypassed by connecting to the IP directly.

Caddy is recreated when the ranges change, so a later deploy picks up new
Cloudflare ranges.

### Without an origin certificate

Caddy falls back to Let's Encrypt. Its HTTP-01 challenge reaches Caddy
through Cloudflare, but the very first certificate cannot be obtained while
Cloudflare is in **Full (strict)** mode and redirects HTTP to HTTPS (there is
no valid origin certificate yet). Either use an origin certificate, or for
the first deploy use **Full** mode (or turn off "Always Use HTTPS"), then
switch to **Full (strict)** once the deploy reports healthy.

### Firewall

The Caddy check above is the second line. For the first, restrict ports 80
and 443 in the Lightsail firewall (Networking → IPv4/IPv6 Firewall) to
Cloudflare's ranges; keep SSH (22) open for the workflow.

## Without Cloudflare

Leave `LIGHTSAIL_CLOUDFLARE` unset. A domain gets a Let's Encrypt
certificate (ports 80 and 443 open, DNS pointing at the instance); a bare IP
gets one from Caddy's internal CA (browsers warn).
