# Running Hxvy: what to watch, and what to do when it breaks

## What "working" means (SLIs and SLOs)

For a few dozen friends, simple targets are enough. They are measured from `/metrics` (see DEPLOY.md).

| What people feel | How it is measured (SLI) | Target (SLO), over 30 days |
|---|---|---|
| The app answers | share of requests that are not 5xx: `gateway_requests_total` by `code` | 99.5% |
| The app is quick | p99 request time, from `gateway_request_duration_seconds` | under 1 second |
| Nothing is lost | a nightly backup exists and a restore was tried this quarter | yes |
| Logging in works | share of sign-ins that are not 5xx | 99% |

**Error budget.** 99.5% over 30 days leaves about 3.6 hours of failure a month. While there is budget left,
change things freely. When it is used up, stop adding features and make it stable first.

## Alerts (so you hear about it before your friends do)

- **UptimeRobot** (free): check `https://<your host>/health` every 5 minutes; it messages you when it fails.
- On the server, `docker compose -f docker-compose.prod.yml ps` shows `unhealthy` for a service whose database
  does not answer (each service checks `/ready`, not just that it is running).
- A disk that fills up stops Postgres. Look at `df -h` now and then; Docker logs are rotated (3 x 10 MB each).

## Logs

Every line is JSON in production, one object per request: `req` (the request id), `user`, `method`, `path`,
`status`, `ms`. Follow one request through every service by its id:

```bash
docker compose -f docker-compose.prod.yml logs --since 1h | grep '<request id>'
docker compose -f docker-compose.prod.yml logs --since 1h gateway | grep '"level":"ERROR"'
```

The gateway puts the id in the `X-Request-ID` response header of every answer (the app does not show it yet).

## When something is wrong

1. `curl https://<host>/health`: if the gateway does not answer, look at `docker compose ps` and the logs.
2. Inside the server, `/ready` names the service that cannot do its job.
3. Recent deploy? `./scripts/rollback.sh`.
4. Data damaged? `./scripts/restore.sh <backup folder> <database>` (it stops that service while it restores).
5. Server gone? New server, DEPLOY.md steps 1 to 3 with the **saved `.env`**, then restore each database from the
   copy of `~/hxvy-backups` you kept elsewhere.

## After an incident: a short post-mortem (no blame)

Write it the same day, even for a small one. Keep it in `docs/incidents/YYYY-MM-DD.md`.

```
What happened (one paragraph) and for how long
Who was affected
How it was noticed (an alert, or a friend told you?)  <- if a friend, an alert is missing
Cause
What fixed it
What stops it happening again (one or two small, concrete changes)
```

## Deleted accounts

When someone deletes their account the gateway refuses their tokens at once (an access token would
otherwise live up to 15 minutes), and from the moment deletion starts nothing can be written for that account.
The gateway asks the auth service whether an account exists (once a minute per person). If the auth service
cannot be reached, people who are signed in keep working until it is back.

## Limits worth knowing (set in the gateway)

A request body over 1 MiB gets 413. A user over 600 requests a minute, or 30 barcode lookups a minute, gets 429.
Sign-in attempts are limited to 20 a minute per IP address. A reused refresh token ends all of that user's
sessions, so a stolen one cannot be used quietly.
