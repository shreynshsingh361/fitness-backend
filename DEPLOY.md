# Deploying Hxvy to a server

One small server runs everything: Caddy (HTTPS), the gateway, the five services and Postgres, all in
Docker Compose. About 2 GB of RAM and 40 GB of disk is plenty for a few dozen people.

Everything below was tried end to end on a laptop (build, start, HTTPS, backup, restore, rollback). It
has **not** yet been run on a real server: expect to fix small things the first time.

## 1. The server

1. Rent an Ubuntu 24.04 server (2 GB RAM). Add your SSH key; do not use a password.
2. Note its IP, for example `203.0.113.7`. The name the app will use needs no domain:
   `api.203-0-113-7.sslip.io` points at that IP and Caddy gets a real HTTPS certificate for it.
3. Install Docker and open only the ports that are needed:

   ```bash
   curl -fsSL https://get.docker.com | sh
   ufw allow OpenSSH && ufw allow 80/tcp && ufw allow 443/tcp && ufw --force enable
   ```

## 2. The code and the secrets

```bash
git clone https://github.com/shreynshsingh361/fitness-backend.git hxvy && cd hxvy
./scripts/make-prod-env.sh api.203-0-113-7.sslip.io hxvy-32756   # your host name, your Firebase project id
```

`make-prod-env.sh` writes `.env` with fresh random secrets (JWT secret and six database passwords).
**Copy `.env` somewhere safe that is not this server.** Without the database passwords a backup cannot be
brought back on a new server. Never commit it.

## 3. Start, then fill the food catalogue

```bash
./scripts/deploy.sh
```

It builds the images, starts everything and waits until `https://<your host>/health` answers. Then load the
foods (a 2 MB file made on your own machine with `./scripts/import-foods.sh --india-only`):

```bash
scp data/foods/foods.india.csv you@server:hxvy/
COMPOSE_FILE=docker-compose.prod.yml ./scripts/load-foods.sh foods.india.csv
```

## 4. Firebase and the app

- Add the SHA-1 of the key that signs the release app to the Android app in the Firebase console, or Google
  sign-in fails on your friends' phones. (`keytool -list -v -keystore <your release keystore>`)
- In Google Cloud Console, set the OAuth consent screen to "In production", or only listed test users can
  sign in with Google.
- Build the app with `--dart-define=API_URL=https://api.203-0-113-7.sslip.io`.

## 5. Every night: a backup

```bash
( crontab -l 2>/dev/null; echo '30 3 * * * cd ~/hxvy && ./scripts/backup.sh >> ~/hxvy-backup.log 2>&1' ) | crontab -
```

Backups go to `~/hxvy-backups` and the last 14 days are kept. **A backup on the same disk does not survive the
disk**: now and then copy that folder to your own computer (`scp -r you@server:hxvy-backups .`).
Try a restore once while nothing depends on it: `./scripts/restore.sh <backup folder> <database>`. It reads
the backup through before touching anything, saves the current database to `~/hxvy-backups/pre-restore-*`,
replaces everything in one transaction (so a failure changes nothing) and starts the service again even if it
fails.

## 6. Updating and going back

```bash
./scripts/deploy.sh     # pulls the latest code, takes a backup first, rebuilds, waits for /health
./scripts/rollback.sh   # back to the version that ran before the last deploy
```

Database changes only go forward. A rollback runs the old code against the new tables, which works because a
new migration only adds things; if a deploy damaged data, restore from the backup instead.

## 7. Changing the JWT secret (rotation)

Do this if the secret may have leaked, or from time to time.

1. In `.env`: copy the current `JWT_SECRET` into `JWT_SECRET_PREVIOUS`, and put a new random value in
   `JWT_SECRET` (`openssl rand -hex 32`).
2. `./scripts/deploy.sh`. People stay signed in: the gateway accepts tokens signed with either secret.
3. After 20 minutes (access tokens live 15), delete `JWT_SECRET_PREVIOUS` from `.env` and deploy again.

## Browsers and CORS

The phone app is not a browser and needs no setting. In production the gateway allows **no** browser origin
unless `CORS_ORIGINS` lists one (for example `CORS_ORIGINS=https://app.example.com` in `.env`); it never falls
back to `localhost`.

## What the proxy lets through

Caddy passes only `/api/*` and `/health` to the gateway and answers 404 to everything else, including
`/metrics`, `/ready` and `/health/services`. Read those from the server itself:

```bash
docker compose -f docker-compose.prod.yml exec gateway wget -qO- http://127.0.0.1:8080/ready
docker compose -f docker-compose.prod.yml exec gateway wget -qO- http://127.0.0.1:8080/metrics
```
