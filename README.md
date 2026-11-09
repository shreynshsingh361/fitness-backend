# FitTrack backend

The API side of FitTrack (steps and social challenges, workouts, food diary, water): an **API gateway** and five
**Go microservices** (auth, workout, nutrition, activity, social), each with its own PostgreSQL database. There is no UI
in this project.

The web app is a **completely separate project**, `fitness-frontend`, with its own folder, its own dependencies and its
own Docker setup. The two only talk over HTTP, and the only thing the frontend needs to know is the address of the gateway.
A mobile app could use the same API later.

Based on the technology blueprint (Go + Gin + PostgreSQL), but split into microservices instead of the blueprint's
modular monolith. See [Differences from the blueprint](#differences-from-the-blueprint).

## Run it

You need Docker Desktop (running). From this folder:

```bash
docker compose up --build
```

The first build downloads images and takes a few minutes. Then:

| What | Where |
|---|---|
| The API (gateway) | http://localhost:8080 |
| Are all services alive? | http://localhost:8080/health/services |
| The API contract | [`contracts/openapi.yaml`](contracts/openapi.yaml) |
| Look inside a database | `docker compose exec postgres psql -U postgres -d workout_db` (then try `\dt` and `SELECT * FROM workouts;`) |

To check the whole backend with real HTTP calls (no frontend needed), run the smoke test in a second terminal:

```bash
./scripts/smoke-test.sh
```

Stop with `Ctrl+C`; wipe all data with `docker compose down -v`.

## Using it from a frontend

1. Point the frontend at `http://localhost:8080` (in `fitness-frontend` that is `NEXT_PUBLIC_API_URL`).
2. Browsers only allow a page to call this API if the gateway lists the page's address. The defaults are
   `http://localhost:3000` and `http://127.0.0.1:3000`. For another address, set `CORS_ORIGINS` (see `.env.example`).
3. Every endpoint, request and response is described in [`contracts/openapi.yaml`](contracts/openapi.yaml). That file is
   the agreement between the two projects; change it first when the API changes.

## How the pieces fit

```
  any client (web app, mobile app, curl)
     │
     │   HTTP + JSON
     ▼
  gateway :8080   (Go)                ← the ONLY port the outside world can reach
     │   checks the login token, then forwards to whichever service owns the URL
     ├──────────────► auth-service      :8081 ──► auth_db       users, refresh tokens
     ├──────────────► workout-service   :8082 ──► workout_db    exercises, workouts, sets
     ├──────────────► nutrition-service :8083 ──► nutrition_db  foods, diary, goals, water
     ├──────────────► activity-service  :8084 ──► activity_db   step counts, step goal, streaks, achievements
     └──────────────► social-service    :8085 ──► social_db     friends, hiding, cheers, groups, chat
                          │  asks activity-service for step totals, auth-service for names
                          ▼  (over their /internal HTTP routes, never their databases)
                       (no ports published: only reachable from inside Docker)
```

| Project | Folder | Own go.mod | Own database | Owns |
|---|---|---|---|---|
| Gateway | [`gateway/`](gateway) | yes | none | login-token check, routing, CORS, the dashboard |
| Auth service | [`services/auth-service/`](services/auth-service) | yes | `auth_db` | accounts, passwords, tokens |
| Workout service | [`services/workout-service/`](services/workout-service) | yes | `workout_db` | exercises, workouts, sets, records |
| Nutrition service | [`services/nutrition-service/`](services/nutrition-service) | yes | `nutrition_db` | foods, diary, calorie goal, water |
| Activity service | [`services/activity-service/`](services/activity-service) | yes | `activity_db` | step counts by source, step goal, streaks, records, achievements, country rank |
| Social service | [`services/social-service/`](services/social-service) | yes | `social_db` | friends, hiding, cheers and taunts, pace bots, groups, leaderboards, chat, reports |

The rules that make this *microservices* rather than one program in several folders:

1. **No shared code.** Each service is built and deployed alone, from its own folder. A few small helpers
   (error format, migration runner) are *copied* between services on purpose; a shared library would couple them.
2. **Database per service.** Each service has its own Postgres login that can only open its own database
   ([`infra/postgres/init.sql`](infra/postgres/init.sql)). The workout service *cannot* read the users table.
   There is no foreign key from `workouts.user_id` to a user: it is just an id the auth service handed out.
3. **Talk over HTTP only.** Clients never talk to a service directly. A service that needs another's data asks over
   HTTP on that service's `/internal` routes and never touches its database: a leaderboard takes members from the social
   service, steps from activity and names from auth. Where one screen spans services, the gateway combines them (see
   `/api/v1/dashboard`).
4. **One public door.** Only the gateway publishes a port to your machine, and its route table has no `/internal`, so
   those routes cannot be reached from outside (paths that try to climb out with `..` are refused too).

### Who is logged in? (the part worth understanding)

1. `POST /auth/login` → the auth service checks the password and returns a short-lived **access token** (a JWT signed with `JWT_SECRET`) and a long-lived, single-use **refresh token**.
2. The client sends `Authorization: Bearer <access token>` with every request.
3. The gateway verifies the signature and expiry. It **deletes any `X-User-ID` header the client sent**, then sets its own from the token.
4. The services trust `X-User-ID` and use it in every SQL query (`WHERE user_id = $1`). They never accept a user id from the request body.

That works only because the services cannot be reached except through the gateway. The smoke test proves both halves: forged headers are ignored, and ports 8081–8085 are closed to the outside.

### Steps and social challenges

The features of a social step-challenge app, and the rules behind them:

- **A day's steps are the highest count of any source, never the sum**, because a phone and a watch count the same walk. Each source is stored separately. A device's count only goes up, so a retried or out-of-order sync is harmless.
- **Each person's day is their own local date.** Clients send it, so it ends at that person's midnight.
- **Private by default.** Only friends and people in a group you joined can see you. Hiding someone works both ways everywhere (boards, members, chat, cheers), and they are never told: anything they may not see simply looks like a 404.
- **Leaderboards are worked out when asked for**, so there is nothing to start, stop or reset. Day, week (Monday to Sunday), month, and an optional fixed-date challenge of 1 day to 1 year. Equal steps share a rank.
- **Mystery mode** shows ranks but hides other people's counts for dates that are not over. A date is revealed once it has ended everywhere on Earth, because someone in another time zone may still be walking it. Reports obey this too.
- **Pace bots** are virtual friends. Nothing is stored: their steps come from their id and the date, and grow through the day.
- **Deleting an account** spans every service with no shared transaction, so the order makes it safe: check the password, delete the data everywhere, delete the login last. If a service is down the login survives, and the person can simply try again.

Where this differs from apps it was modelled on, and what is missing:

- **Steps only come from devices.** The mobile app reads Apple Health (iPhone) or Health Connect (Android) and sends them to `POST /steps/sync`; nobody can type steps in, so boards cannot be padded by hand. A web page cannot read any of these, so the web app only shows steps.
- **Not built:** push notifications, direct Fitbit/Garmin/Samsung connections, team-against-team boards, a prize draw, live chat (the web app re-fetches every 5 seconds). Changing your step goal also re-judges past streaks, because goal history is not stored.

### Following one request through the logs

```bash
docker compose logs -f gateway workout-service
```

Every request gets an id at the gateway (`X-Request-ID`) that appears in each service's log line, so you can see
`req=3f2a…` enter the gateway, then show up in the workout service.

## Food database

The nutrition service starts with 30 foods of its own. To load the open food databases:

```bash
./scripts/import-foods.sh             # USDA FoodData Central + Open Food Facts
./scripts/import-foods.sh --usda-only # just the ~8,000 generic foods (14 MB download)
```

- **USDA FoodData Central** (SR Legacy + Foundation): ~8,000 generic foods, public domain.
- **Open Food Facts**: packaged products with a name and complete calories and macros.
  The download is 1.5 GB. Open Database License: apps that show this data must credit
  Open Food Facts (the mobile app does), and a database derived from it that you publish
  must be shared under the same licence.

Rows with impossible numbers (more than 900 kcal per 100 g, macros that don't add up to
the calories) are skipped. Running it again updates foods instead of adding them twice,
and diary entries are unaffected because they keep their own copy of the numbers. The
downloads go to `data/foods/`, which git ignores.

### Recipes

People can make their own dishes from catalogue foods (`/recipes`): a name, how many
servings it makes, optionally the cooked weight, and the ingredients with their amounts.
A recipe is saved as one more food (source `recipe`) that only its maker can see, so food
search finds it (first) and the diary adds it like rice or eggs. One serving is everything
in it divided by the number of servings. Changing or deleting a recipe never changes a day
that was already logged. A recipe cannot contain another recipe.

## Project layout

```
fitness-backend/
├── docker-compose.yml        starts everything
├── contracts/openapi.yaml    the API contract (the agreement with any frontend)
├── infra/postgres/init.sql   creates the 3 databases and their logins
├── scripts/smoke-test.sh     end-to-end check with curl
├── gateway/                  Go: cmd/gateway, internal/{config,gateway}
└── services/<name>-service/  Go: cmd/api, internal/{config,database,store,api,...}
        store/                  all SQL lives here
        api/                    HTTP handlers + validation
        database/migrations/    numbered .sql files, applied at startup
```

## Tests

Each project tests itself. None of these need a database or Docker:

```bash
(cd gateway && go test -race ./...)
(cd services/auth-service && go test ./...)
(cd services/workout-service && go test ./...)
(cd services/nutrition-service && go test ./...)
(cd services/activity-service && go test ./...)
(cd services/social-service && go test ./...)
```

The Go tests cover validation, token handling, routing and header-spoofing. The code that talks to Postgres is
covered by `scripts/smoke-test.sh` against the running system.

The smoke test's last check confirms the five services are **not** reachable from outside, which is only true under
Docker. If you run the services directly on your machine instead, skip that one check:

```bash
CHECK_PORT_ISOLATION=0 ./scripts/smoke-test.sh
```

## Working on one service without Docker

Postgres is not published to your machine by default (a Postgres you installed yourself probably owns port 5432).
Publish ours on another port by adding `ports: ["127.0.0.1:5433:5432"]` to the `postgres` service in
`docker-compose.yml`, start only the database, then run the service on your machine:

```bash
docker compose up -d postgres
cd services/workout-service
DATABASE_URL=postgres://workout_svc:workout_dev_password@localhost:5433/workout_db go run ./cmd/api
```

Run the others the same way (auth also needs `JWT_SECRET`, at least 16 characters, identical in the gateway), then the gateway
(`JWT_SECRET=... go run ./cmd/gateway`). Directly on your machine the services listen on `localhost`, so you can `curl` them
with a header like `-H "X-User-ID: <some-uuid>"`. Inside Docker they are walled off.

## Adding another service (for example "activity")

1. Copy a service folder, rename the Go module, write its migrations and handlers.
2. Add a role and database for it in `infra/postgres/init.sql` (then `docker compose down -v` to re-run it).
3. Add it to `docker-compose.yml` (no `ports:`), and give the gateway its URL.
4. Add its path prefixes to the route table in `gateway/internal/gateway/gateway.go`.
5. Document it in `contracts/openapi.yaml`. The frontend project then adds its screens separately.

## Differences from the blueprint

The blueprint recommends a **modular monolith** for the first release and warns that microservices add cost before they are
needed. You asked for microservices, so this project follows the blueprint's *module boundaries* (auth, workout, nutrition, …) but
makes each one a deployable service. What that costs you, honestly:

- More moving parts: seven containers instead of one, and a gateway to maintain.
- No database joins or transactions across services. "Delete my account" really is a careful multi-step process (`gateway/internal/gateway/account.go`), and one leaderboard request makes three HTTP calls where a monolith would run one SQL join.
- Failures between services (a service down, a slow call). The dashboard shows partial data; a leaderboard answers 503 without step data.

| Blueprint | Here | Why |
|---|---|---|
| Flutter mobile app | not in this project | the web client is the separate `fitness-frontend`; a Flutter client can use the same REST API later |
| `sqlc` generated queries | hand-written SQL with `pgx` | simpler to read while learning |
| `golang-migrate` / Goose | ~40-line migration runner per service | same idea (numbered SQL files, run once), fewer dependencies |
| Redis cache and rate limits | not included | no proven need yet, as the blueprint says |
| Offline workout logging with Drift/SQLite | not included | `client_op_id` idempotency is in place so it can be added |
| Device sync on the phone | API only | `POST /steps/sync` exists, but it needs the mobile app to call it |
| Subscriptions, admin panel | not built | the rest of phases 6–7 of the blueprint's build order |

Done so far, by the blueprint's build order: **1 Foundation, 2 Authentication, 3 Workout MVP, 4 Nutrition MVP, 5 Activity MVP, and the challenges half of 6** (online only).

## Before this goes anywhere near production

This is a solid learning/starting point, not a hardened deployment:

- **Secrets.** `JWT_SECRET` and the database passwords in `docker-compose.yml` / `init.sql` are development defaults. Use a secrets manager.
- **HTTPS.** Everything here is plain HTTP on localhost. Put TLS in front of the gateway.
- **Signing keys.** The auth service and the gateway share one HS256 secret, so the gateway *could* mint tokens. A stronger design signs with a private key (RS256/EdDSA) in the auth service only, and the gateway holds just the public key.
- **Rate limiting** on `/auth/login` and `/auth/register` (the blueprint puts this in Redis).
- **Refresh-token theft detection.** A reused refresh token is rejected, but the user's other sessions are not revoked.
- **Email verification and password reset** are not implemented.
- **After an account is deleted**, its access token keeps passing the gateway until it expires (up to 15 minutes), because tokens are not checked against a list.
- **Large groups.** Leaderboards read every member's totals on each request, with no caching. Fine for a small group; a real 15,000-member group needs a cache (the blueprint's Redis).
- **Internal routes have no login of their own.** They rely on the network being closed, like `X-User-ID`. A stronger setup adds service-to-service authentication.
- **Food data** comes from open databases with uneven quality, especially Open Food Facts. Spot-check foods people log often.

## Troubleshooting

| Problem | Try |
|---|---|
| Something does not start | `docker compose ps`, then `docker compose logs <service>` |
| Every container stays in "Created" and nothing ever starts (even a plain `docker run alpine echo hi` hangs) | Docker Desktop itself is stuck, not this project. Restart it (`docker desktop restart`) and run `docker compose up` again |
| Port 8080 already in use | stop the other program, or change the left side of that `ports:` line in `docker-compose.yml` |
| Changed `init.sql` and nothing happened | init scripts run only on a fresh volume: `docker compose down -v` |
| The frontend says "Cannot reach the server" | open http://localhost:8080/health/services: if that works, check `CORS_ORIGINS` includes the frontend's address |
