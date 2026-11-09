#!/usr/bin/env bash
# End-to-end smoke test for the whole system, using only curl and jq.
#
#   1. docker compose up --build -d        (start everything)
#   2. ./scripts/smoke-test.sh
#
# It talks to the GATEWAY exactly like the browser does. Every check prints
# PASS or FAIL, so you can read the output as a tour of what the API does.
# Each run creates two fresh users, so it is safe to run repeatedly.

set -u

GATEWAY="${GATEWAY:-http://localhost:8080}"
API="$GATEWAY/api/v1"

command -v jq >/dev/null || { echo "This script needs jq (brew install jq)."; exit 2; }

BODY_FILE=$(mktemp)
HEADERS_FILE=$(mktemp)
trap 'rm -f "$BODY_FILE" "$HEADERS_FILE"' EXIT

STATUS=""
passed=0
failed=0

if [ -t 1 ]; then GREEN=$'\033[32m'; RED=$'\033[31m'; BOLD=$'\033[1m'; RESET=$'\033[0m'; else GREEN=""; RED=""; BOLD=""; RESET=""; fi

ok()  { passed=$((passed + 1)); echo "  ${GREEN}PASS${RESET} $1"; }
bad() { failed=$((failed + 1)); echo "  ${RED}FAIL${RESET} $1"; }
section() { echo; echo "${BOLD}$1${RESET}"; }

# call METHOD PATH [JSON_BODY] [ACCESS_TOKEN] [EXTRA_HEADER]
# Sets $STATUS (HTTP status) and leaves the response body in $BODY_FILE.
call() {
  local method=$1 path=$2 body=${3:-} token=${4:-} extra=${5:-}
  local args=(-sS -o "$BODY_FILE" -D "$HEADERS_FILE" -w '%{http_code}' -X "$method" "$API$path")
  [ -n "$body" ]  && args+=(-H 'Content-Type: application/json' -d "$body")
  [ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
  [ -n "$extra" ] && args+=(-H "$extra")
  STATUS=$(curl "${args[@]}" 2>&1) || STATUS="curl-failed"
}

# status "what we checked" EXPECTED_STATUS
status() {
  if [ "$STATUS" = "$2" ]; then ok "$1"; else bad "$1 (expected HTTP $2, got $STATUS: $(head -c 300 "$BODY_FILE"))"; fi
}

# json "what we checked" JQ_EXPRESSION EXPECTED_VALUE
json() {
  local got
  got=$(jq -r "$2" "$BODY_FILE" 2>/dev/null)
  if [ "$got" = "$3" ]; then ok "$1"; else bad "$1 (expected $3, got $got)"; fi
}

get() { jq -r "$1" "$BODY_FILE"; }

# days_ago N prints the date N days ago (macOS and Linux spell this differently).
days_ago() { date -u -v-"$1"d +%F 2>/dev/null || date -u -d "$1 days ago" +%F; }

RUN=$(date +%s)
EMAIL_A="alice-$RUN@example.com"
EMAIL_B="bob-$RUN@example.com"
EMAIL_C="carol-$RUN@example.com"
EMAIL_D="dave-$RUN@example.com"
PASSWORD="correct-horse-battery"
TODAY=$(date -u +%F)
YESTERDAY=$(days_ago 1)
LONG_AGO=$(days_ago 30)
BRISK_BOT="00000000-0000-4000-8000-00000000b001"

# ---------------------------------------------------------------------------
section "1. Is everything up?"
# /health lives outside /api/v1, so these two use curl directly instead of call().
STATUS=$(curl -sS -o "$BODY_FILE" -w '%{http_code}' "$GATEWAY/health" 2>&1) || STATUS="curl-failed"
status "gateway /health answers" 200
STATUS=$(curl -sS -o "$BODY_FILE" -w '%{http_code}' "$GATEWAY/health/services" 2>&1) || STATUS="curl-failed"
status "gateway can reach every service" 200
json "  status is ok" '.status' ok
if [ "$STATUS" != "200" ] || [ "$(get .status)" != "ok" ]; then
  echo; echo "${RED}The system is not healthy; run: docker compose ps${RESET}"; exit 1
fi

# ---------------------------------------------------------------------------
section "2. Accounts and login (auth-service)"
call POST /auth/register "{\"name\":\"Alice Test\",\"email\":\"$EMAIL_A\",\"password\":\"$PASSWORD\"}"
status "register Alice" 201
ACCESS_A=$(get .access_token); REFRESH_A=$(get .refresh_token); ID_A=$(get .user.id)
json "  response never contains the password hash" '.user | has("password_hash") or has("PasswordHash")' false

call POST /auth/register "{\"name\":\"Alice Again\",\"email\":\"$EMAIL_A\",\"password\":\"$PASSWORD\"}"
status "registering the same email again is refused" 409
json "  error has a machine-readable code" '.code' email_taken

call POST /auth/register '{"name":"","email":"nope","password":"short"}'
status "invalid registration is rejected" 422
json "  every bad field is reported" '.details | keys | join(",")' "email,name,password"

call POST /auth/login "{\"email\":\"$EMAIL_A\",\"password\":\"wrong-password\"}"
status "login with a wrong password" 401
call POST /auth/login "{\"email\":\"nobody-$RUN@example.com\",\"password\":\"$PASSWORD\"}"
status "login with an unknown email gives the same answer" 401
json "  (same message, so emails cannot be probed)" '.code' invalid_credentials

call POST /auth/login "{\"email\":\"$EMAIL_A\",\"password\":\"$PASSWORD\"}"
status "login with the right password" 200
ACCESS_A=$(get .access_token); REFRESH_A=$(get .refresh_token)

# ---------------------------------------------------------------------------
section "3. The gateway guards the door"
call GET /auth/me
status "no token -> 401" 401
call GET /auth/me "" "not.a.real.token"
status "garbage token -> 401" 401
call GET /workouts "" "$ACCESS_A"
status "valid token -> allowed through" 200
call GET /auth/me "" "$ACCESS_A"
status "GET /auth/me with a valid token" 200
json "  returns Alice" '.email' "$EMAIL_A"
if grep -qi '^x-request-id: ' "$HEADERS_FILE"; then ok "every response carries an X-Request-ID"; else bad "X-Request-ID header missing"; fi
call GET /does-not-exist "" "$ACCESS_A"
status "unknown endpoint -> 404" 404

# ---------------------------------------------------------------------------
section "4. Workouts (workout-service)"
call GET "/exercises?search=squat&limit=5" "" "$ACCESS_A"
status "search the exercise catalogue" 200
json "  finds Back Squat" '[.items[].name] | index("Back Squat") != null' true
SQUAT=$(jq -r '.items[] | select(.name=="Back Squat") | .id' "$BODY_FILE")

call GET "/exercises?limit=1000" "" "$ACCESS_A"
json "  page size is capped at 100" '.limit' 100

OP_W=$(uuidgen | tr 'A-Z' 'a-z')
WORKOUT="{\"client_op_id\":\"$OP_W\",\"name\":\"Leg day\",\"duration_minutes\":50,\"sets\":[{\"exercise_id\":\"$SQUAT\",\"reps\":5,\"weight_kg\":100},{\"exercise_id\":\"$SQUAT\",\"reps\":5,\"weight_kg\":105}]}"
call POST /workouts "$WORKOUT" "$ACCESS_A"
status "create a workout" 201
WORKOUT_ID=$(get .id)
json "  volume = 5x100 + 5x105 = 1025 kg" '.volume_kg' 1025

call POST /workouts "$WORKOUT" "$ACCESS_A"
status "sending the SAME request again (a retry) returns the saved workout" 200
json "  ...with the same id, nothing duplicated" '.id' "$WORKOUT_ID"

call GET /workouts "" "$ACCESS_A"
json "history has exactly one workout" '.total' 1

call GET "/workouts/$WORKOUT_ID" "" "$ACCESS_A"
status "fetch one workout" 200
json "  with its two sets, in order" '[.sets[].weight_kg] | join(",")' "100,105"

call POST /workouts "{\"client_op_id\":\"$(uuidgen | tr 'A-Z' 'a-z')\",\"name\":\"Bad\",\"sets\":[{\"exercise_id\":\"$SQUAT\",\"reps\":0,\"weight_kg\":-5}]}" "$ACCESS_A"
status "invalid sets are rejected" 422
call POST /workouts "{\"client_op_id\":\"$(uuidgen | tr 'A-Z' 'a-z')\",\"name\":\"Ghost\",\"sets\":[{\"exercise_id\":\"00000000-0000-0000-0000-000000000000\",\"reps\":5,\"weight_kg\":50}]}" "$ACCESS_A"
status "an exercise that does not exist is rejected" 422
call GET /workouts "" "$ACCESS_A"
json "  and the failed attempts saved nothing" '.total' 1

call GET /workouts/summary "" "$ACCESS_A"
json "7-day summary counts the workout" '.workouts' 1
call GET /records "" "$ACCESS_A"
json "personal record is the heaviest set (105 kg)" '.items[0].weight_kg' 105

# ---------------------------------------------------------------------------
section "5. Nutrition (nutrition-service)"
call GET "/foods?search=egg" "" "$ACCESS_A"
status "search foods" 200
EGG=$(jq -r '.items[] | select(.name=="Egg, whole") | .id' "$BODY_FILE")

OP_F=$(uuidgen | tr 'A-Z' 'a-z')
ENTRY="{\"client_op_id\":\"$OP_F\",\"food_id\":\"$EGG\",\"meal\":\"breakfast\",\"servings\":2,\"eaten_on\":\"$TODAY\"}"
call POST /diary "$ENTRY" "$ACCESS_A"
status "log 2 eggs for breakfast" 201
ENTRY_ID=$(get .id)
json "  nutrition is a snapshot, scaled by servings (2 x 72)" '.calories' 144
call POST /diary "$ENTRY" "$ACCESS_A"
status "the same request again is not added twice" 200
json "  ...same entry id" '.id' "$ENTRY_ID"

call GET "/diary?date=$TODAY" "" "$ACCESS_A"
json "diary total is 144 kcal" '.totals.calories' 144
json "  with exactly one entry" '.entries | length' 1

call POST /diary "{\"client_op_id\":\"$(uuidgen | tr 'A-Z' 'a-z')\",\"food_id\":\"$EGG\",\"meal\":\"brunch\",\"servings\":1}" "$ACCESS_A"
status "an invalid meal name is rejected" 422

call GET /goals "" "$ACCESS_A"
json "default calorie goal" '.calories' 2000
call PUT /goals '{"calories":2400}' "$ACCESS_A"
status "set a calorie goal" 200
call PUT /goals '{"calories":50}' "$ACCESS_A"
status "an unreasonable goal is rejected" 422

call POST /water "{\"amount_ml\":250,\"date\":\"$TODAY\"}" "$ACCESS_A"
status "log 250 ml of water" 201
call POST /water "{\"amount_ml\":500,\"date\":\"$TODAY\"}" "$ACCESS_A"
json "  water adds up (250 + 500)" '.water_ml' 750

call GET "/nutrition/summary?date=$TODAY" "" "$ACCESS_A"
json "day summary: eaten" '.calories_eaten' 144
json "  goal" '.calories_goal' 2400
json "  water" '.water_ml' 750

# ---------------------------------------------------------------------------
section "6. The dashboard (gateway combines three services)"
call GET "/dashboard?date=$TODAY" "" "$ACCESS_A"
status "one request, three services" 200
json "workout data is included" '.workouts.workouts' 1
json "nutrition data is included" '.nutrition.calories_eaten' 144
json "step data is included" '.steps.goal' 10000
json "nothing is marked unavailable" '.unavailable | length' 0

# ---------------------------------------------------------------------------
section "7. Users cannot see each other's data"
call POST /auth/register "{\"name\":\"Bob Test\",\"email\":\"$EMAIL_B\",\"password\":\"$PASSWORD\"}"
status "register Bob" 201
ACCESS_B=$(get .access_token); ID_B=$(get .user.id)

call GET /workouts "" "$ACCESS_B"
json "Bob's history is empty" '.total' 0
call GET "/workouts/$WORKOUT_ID" "" "$ACCESS_B"
status "Bob cannot open Alice's workout (looks like 'not found')" 404
call DELETE "/workouts/$WORKOUT_ID" "" "$ACCESS_B"
status "Bob cannot delete Alice's workout" 404
call DELETE "/diary/$ENTRY_ID" "" "$ACCESS_B"
status "Bob cannot delete Alice's diary entry" 404
call GET "/diary?date=$TODAY" "" "$ACCESS_B"
json "Bob's diary is empty" '.totals.calories' 0

call GET /auth/me "" "$ACCESS_A" "X-User-ID: $ID_B"
status "Alice sends a forged X-User-ID header claiming to be Bob" 200
json "  the gateway ignores it: she is still Alice" '.email' "$EMAIL_A"

call GET /workouts "" "$ACCESS_A" "X-User-ID: $ID_B"
json "  and her workouts are still hers" '.total' 1

# ---------------------------------------------------------------------------
section "8. Refresh tokens and logout"
call POST /auth/refresh "{\"refresh_token\":\"$REFRESH_A\"}"
status "trade the refresh token for a new pair" 200
NEW_ACCESS=$(get .access_token); NEW_REFRESH=$(get .refresh_token)
if [ "$NEW_REFRESH" != "$REFRESH_A" ]; then ok "  the refresh token was rotated"; else bad "refresh token was not rotated"; fi

call POST /auth/refresh "{\"refresh_token\":\"$REFRESH_A\"}"
status "the OLD refresh token no longer works (single use)" 401

call GET /workouts "" "$NEW_ACCESS"
status "the new access token works" 200

call POST /auth/logout "{\"refresh_token\":\"$NEW_REFRESH\"}"
status "logout" 204
call POST /auth/refresh "{\"refresh_token\":\"$NEW_REFRESH\"}"
status "a logged-out refresh token is dead" 401

# ---------------------------------------------------------------------------
section "9. Steps (activity-service)"
call POST /steps/sync "{\"source\":\"apple_health\",\"days\":[{\"date\":\"$TODAY\",\"steps\":8000},{\"date\":\"$YESTERDAY\",\"steps\":12000}]}" "$ACCESS_A"
status "a device syncs two days of steps" 200
json "  both days were saved" '.synced' 2

call GET "/steps/today?date=$TODAY" "" "$ACCESS_A"
status "read today" 200
json "  steps" '.steps' 8000
json "  the default goal is 10,000" '.goal' 10000
json "  goal not reached yet" '.goal_met' false
json "  distance is estimated from steps" '.distance_km' 6.1
json "  calories are estimated from steps" '.calories' 320

call POST /steps/sync "{\"source\":\"apple_health\",\"days\":[{\"date\":\"$TODAY\",\"steps\":5000}]}" "$ACCESS_A"
call GET "/steps/today?date=$TODAY" "" "$ACCESS_A"
json "a late retry with a lower count changes nothing" '.steps' 8000

call POST /steps/sync "{\"source\":\"apple_health\",\"days\":[{\"date\":\"$TODAY\",\"steps\":9000}]}" "$ACCESS_A"
call POST /steps/sync "{\"source\":\"phone\",\"days\":[{\"date\":\"$TODAY\",\"steps\":7000}]}" "$ACCESS_A"
call GET "/steps/today?date=$TODAY" "" "$ACCESS_A"
json "a phone and a watch are not added together (highest wins)" '.steps' 9000
json "  both sources are remembered" '.sources | length' 2

call PUT /steps/manual "{\"date\":\"$TODAY\",\"steps\":9500}" "$ACCESS_A"
status "steps cannot be typed in by hand" 404
call POST /steps/sync "{\"source\":\"manual\",\"days\":[{\"date\":\"$TODAY\",\"steps\":100}]}" "$ACCESS_A"
status "  not even disguised as a device" 422
call POST /steps/sync '{"source":"fitbit","days":[{"date":"2099-01-01","steps":100}]}' "$ACCESS_A"
status "steps in the future are rejected" 422
call POST /steps/sync "{\"source\":\"phone\",\"days\":[{\"date\":\"$TODAY\",\"steps\":-5}]}" "$ACCESS_A"
status "negative steps are rejected" 422

call GET "/steps/history?from=$YESTERDAY&to=$TODAY" "" "$ACCESS_A"
status "history for a date range" 200
json "  total" '.total_steps' 21000
json "  average" '.average_steps' 10500
call GET "/steps/history?from=$(days_ago 3)&to=$TODAY" "" "$ACCESS_A"
json "  days with no steps are included, so charts have no holes" '.days | length' 4

call GET /steps/settings "" "$ACCESS_A"
json "the goal starts at the default" '.is_default' true
call PUT /steps/settings '{"daily_goal":9000,"country":"in"}' "$ACCESS_A"
status "set a goal and a country" 200
json "  the country is normalized" '.country' IN
call PUT /steps/settings '{"daily_goal":10}' "$ACCESS_A"
status "an unreasonable goal is rejected" 422
call PUT /steps/settings '{"daily_goal":9000,"country":"India"}' "$ACCESS_A"
status "a country name instead of a code is rejected" 422

call GET "/steps/today?date=$TODAY" "" "$ACCESS_A"
json "the new goal is now reached" '.goal_met' true
json "  and the streak counts yesterday too" '.current_streak' 2

call GET "/steps/stats?date=$TODAY" "" "$ACCESS_A"
status "streaks, records and achievements" 200
json "  longest streak" '.longest_streak' 2
json "  best day" '.best_day.steps' 12000
json "  an achievement is earned" '[.achievements[] | select(.id == "first_steps")][0].earned' true
json "  one not earned yet" '[.achievements[] | select(.id == "streak_30")][0].earned' false
json "  country rank shows only your own position" '.country_rank | keys | join(",")' "country,from,rank,to,top_100,total_users"
json "  in your country" '.country_rank.country' IN

call GET "/steps/today?date=$TODAY" "" "$ACCESS_B"
json "Bob does not see Alice's steps as his" '.steps' 0
call GET "/dashboard?date=$TODAY" "" "$ACCESS_A"
json "the dashboard shows the steps" '.steps.steps' 9000

# ---------------------------------------------------------------------------
section "10. Friends, hiding, cheers and taunts (social-service)"
call POST /auth/register "{\"name\":\"Carol Test\",\"email\":\"$EMAIL_C\",\"password\":\"$PASSWORD\"}"
status "register Carol (a stranger to Alice and Bob)" 201
ACCESS_C=$(get .access_token); ID_C=$(get .user.id)

call GET /friends/invite "" "$ACCESS_A"
status "Alice gets her invite code" 200
INVITE_A=$(get .code)
call GET /friends/invite "" "$ACCESS_A"
json "  it stays the same" '.code' "$INVITE_A"

call GET "/friends/invite/$INVITE_A" "" "$ACCESS_B"
status "Bob looks at the invite first" 200
json "  it says who sent it" '.inviter_name' "Alice Test"
call GET "/friends/invite/not-a-code" "" "$ACCESS_B"
status "a made-up invite is not found" 404

call POST /friends/accept "{\"code\":\"$INVITE_A\"}" "$ACCESS_A"
status "Alice cannot accept her own invite" 422
call POST /friends/accept "{\"code\":\"$INVITE_A\"}" "$ACCESS_B"
status "Bob accepts" 201
call POST /friends/accept "{\"code\":\"$INVITE_A\"}" "$ACCESS_B"
status "accepting twice changes nothing" 200

call GET "/friends?date=$TODAY" "" "$ACCESS_B"
status "the friends board" 200
json "  Bob can now see Alice's steps" "[.items[] | select(.user_id == \"$ID_A\")][0].steps" 9000
json "  two pace bots are there too" '[.items[] | select(.is_bot)] | length' 2
json "  Bob's own row is marked" '[.items[] | select(.is_me)][0].user_id' "$ID_B"

call GET "/friends?date=$TODAY" "" "$ACCESS_C"
json "a stranger sees only herself and the bots" '[.items[] | select(.is_bot | not)] | length' 1
call GET "/friends?date=$TODAY&minutes=0" "" "$ACCESS_C"
json "at midnight the bots have not walked yet" '[.items[] | select(.is_bot) | .steps] | add' 0
call GET "/friends?date=$TODAY&minutes=1440" "" "$ACCESS_C"
json "  by the end of the day they have" '[.items[] | select(.is_bot) | .steps] | add > 0' true
call GET "/friends?period=year" "" "$ACCESS_C"
status "an unknown period is rejected" 422

call POST /nudges "{\"to_user_id\":\"$ID_A\",\"kind\":\"cheer\"}" "$ACCESS_B"
status "Bob cheers Alice" 201
call POST /nudges "{\"to_user_id\":\"$ID_A\",\"kind\":\"cheer\"}" "$ACCESS_B"
status "  but not again straight away" 429
call POST /nudges "{\"to_user_id\":\"$ID_A\",\"kind\":\"taunt\"}" "$ACCESS_B"
status "Bob taunts Alice" 201
call POST /nudges "{\"to_user_id\":\"$ID_A\",\"kind\":\"cheer\"}" "$ACCESS_C"
status "a stranger cannot send one" 404
call POST /nudges "{\"to_user_id\":\"$ID_A\",\"kind\":\"cheer\"}" "$ACCESS_A"
status "you cannot cheer yourself" 422
call POST /nudges "{\"to_user_id\":\"$BRISK_BOT\",\"kind\":\"cheer\"}" "$ACCESS_A"
status "bots do not read these" 422

call GET /nudges "" "$ACCESS_A"
status "Alice reads her cheers and taunts" 200
json "  two are unread" '.unread' 2
json "  it says who sent them" '.items[0].from_name' "Bob Test"
call POST /nudges/read "" "$ACCESS_A"
status "  mark them read" 204
call GET /nudges "" "$ACCESS_A"
json "  none are unread now" '.unread' 0

call POST "/friends/$ID_B/hide" "" "$ACCESS_A"
status "Alice hides Bob" 204
call GET "/friends?date=$TODAY" "" "$ACCESS_A"
json "  Alice no longer sees Bob" "[.items[] | select(.user_id == \"$ID_B\")] | length" 0
call GET "/friends?date=$TODAY" "" "$ACCESS_B"
json "  and Bob no longer sees Alice" "[.items[] | select(.user_id == \"$ID_A\")] | length" 0
call POST /nudges "{\"to_user_id\":\"$ID_A\",\"kind\":\"cheer\"}" "$ACCESS_B"
status "  Bob cannot reach her (and just sees 'not found')" 404
call GET /friends/hidden "" "$ACCESS_A"
json "  Alice's hidden list shows him" '.items[0].name' "Bob Test"
call GET /friends/hidden "" "$ACCESS_B"
json "  Bob cannot tell that he was hidden" '.items | length' 0
call POST "/friends/$ID_A/hide" "" "$ACCESS_A"
status "you cannot hide yourself" 422

call POST "/friends/$BRISK_BOT/hide" "" "$ACCESS_A"
status "a bot can be hidden too" 204
call GET "/friends?date=$TODAY" "" "$ACCESS_A"
json "  one bot left" '[.items[] | select(.is_bot)] | length' 1
call DELETE "/friends/$BRISK_BOT/hide" "" "$ACCESS_A"
status "  and brought back" 204

call DELETE "/friends/$ID_B/hide" "" "$ACCESS_A"
status "Alice stops hiding Bob" 204
call GET "/friends?date=$TODAY" "" "$ACCESS_A"
json "  she sees him again" "[.items[] | select(.user_id == \"$ID_B\")] | length" 1
call DELETE "/friends/$ID_B/hide" "" "$ACCESS_A"
status "  there is nothing left to undo" 404

call POST /friends/accept "{\"code\":\"$INVITE_A\"}" "$ACCESS_C"
status "Carol becomes a friend" 201
call DELETE "/friends/$ID_C" "" "$ACCESS_A"
status "Alice removes Carol" 204
call GET "/friends?date=$TODAY" "" "$ACCESS_C"
json "  Carol no longer sees Alice" "[.items[] | select(.user_id == \"$ID_A\")] | length" 0
call DELETE "/friends/$ID_C" "" "$ACCESS_A"
status "  removing twice is not found" 404

# ---------------------------------------------------------------------------
section "11. Groups and leaderboards (social-service asks activity and auth)"
call POST /groups '{"name":"  "}' "$ACCESS_A"
status "a group needs a name" 422
call POST /groups '{"name":"Office Walkers","description":"Lunchtime steps"}' "$ACCESS_A"
status "Alice creates a group" 201
GROUP=$(get .id); GROUP_CODE=$(get .invite_code)
json "  she is its admin" '.my_role' admin
json "  and its only member" '.member_count' 1

call GET "/groups/invite/$GROUP_CODE" "" "$ACCESS_B"
status "Bob looks at the invite" 200
json "  it names the group" '.name' "Office Walkers"
json "  he is not in it yet" '.is_member' false
call POST /groups/join "{\"code\":\"$GROUP_CODE\"}" "$ACCESS_B"
status "Bob joins with the code" 201
json "  as a plain member" '.my_role' member
call POST /groups/join "{\"code\":\"$GROUP_CODE\"}" "$ACCESS_B"
status "joining twice changes nothing" 200
call POST /groups/join '{"code":"not-a-code"}' "$ACCESS_B"
status "a made-up code is not found" 404

call GET "/groups/$GROUP" "" "$ACCESS_C"
status "an outsider cannot see the group exists" 404
call GET "/groups/$GROUP/leaderboard" "" "$ACCESS_C"
status "  or its leaderboard" 404
call GET /groups "" "$ACCESS_B"
json "the group is in Bob's list" "[.items[] | select(.id == \"$GROUP\")] | length" 1

BOARD="/groups/$GROUP/leaderboard"
call GET "$BOARD?period=day&date=$TODAY" "" "$ACCESS_A"
status "today's leaderboard" 200
json "  two people" '.total' 2
json "  Alice leads" '.items[0].name' "Alice Test"
json "  with her steps from the activity service" '.items[0].steps' 9000
json "  and her name from the auth service" '.me.rank' 1

call POST /steps/sync "{\"source\":\"fitbit\",\"days\":[{\"date\":\"$TODAY\",\"steps\":20000}]}" "$ACCESS_B"
call GET "$BOARD?period=day&date=$TODAY" "" "$ACCESS_A"
json "Bob walks more and takes the lead, with nothing to reset" '.items[0].name' "Bob Test"
json "  Alice drops to second" '.me.rank' 2
call GET "$BOARD?period=day&date=$TODAY&limit=1&page=2" "" "$ACCESS_A"
json "leaderboards are paged" '.items[0].name' "Alice Test"
json "  and always include your own row" '.me.rank' 2
call GET "$BOARD?period=week&date=$TODAY" "" "$ACCESS_A"
status "the weekly board" 200
json "  covers at least today" '.me.steps >= 9000' true
call GET "$BOARD?period=month&date=$TODAY" "" "$ACCESS_A"
status "the monthly board" 200
call GET "$BOARD?period=day&date=$LONG_AGO" "" "$ACCESS_A"
json "people with equal steps share a rank" '[.items[].rank] | join(",")' "1,1"
call GET "$BOARD?period=year" "" "$ACCESS_A"
status "an unknown period is rejected" 422

call PATCH "/groups/$GROUP" '{"name":"Taken over"}' "$ACCESS_B"
status "a plain member cannot change settings" 403
call PATCH "/groups/$GROUP" '{"name":"Office Striders"}' "$ACCESS_A"
status "the admin can" 200
json "  the name changed" '.name' "Office Striders"
json "  and nothing else did" '.chat_enabled' true


# ---------------------------------------------------------------------------
section "12. Mystery mode"
call PATCH "/groups/$GROUP" '{"mystery_mode":true}' "$ACCESS_A"
status "the admin turns it on" 200
call GET "$BOARD?period=day&date=$TODAY" "" "$ACCESS_A"
json "the board says so" '.mystery_active' true
json "ranks still show" '[.items[].rank] | join(",")' "1,2"
json "Bob's count is hidden from Alice" '.items[0].steps' null
json "  and marked as hidden" '.items[0].steps_hidden' true
json "Alice still sees her own" '.me.steps' 9000
call GET "$BOARD?period=day&date=$TODAY" "" "$ACCESS_B"
json "Alice's count is hidden from Bob" '.items[1].steps' null
json "Bob still sees his own" '.me.steps' 20000
call GET "$BOARD?period=day&date=$LONG_AGO" "" "$ACCESS_A"
json "dates that are over are not hidden" '.mystery_active' false
call PATCH "/groups/$GROUP" '{"mystery_mode":false}' "$ACCESS_A"
call GET "$BOARD?period=day&date=$TODAY" "" "$ACCESS_A"
json "turned off, the counts come back" '.items[0].steps' 20000

# ---------------------------------------------------------------------------
section "13. Fixed-date challenges"
call GET "$BOARD?period=challenge" "" "$ACCESS_A"
status "no challenge yet" 422
json "  says why" '.code' no_challenge
call PUT "/groups/$GROUP/challenge" "{\"start\":\"$YESTERDAY\",\"end\":\"$TODAY\"}" "$ACCESS_B"
status "a plain member cannot set one" 403
call PUT "/groups/$GROUP/challenge" "{\"start\":\"$TODAY\",\"end\":\"$YESTERDAY\"}" "$ACCESS_A"
status "it cannot end before it starts" 422
call PUT "/groups/$GROUP/challenge" '{"start":"2026-01-01","end":"2028-01-01"}' "$ACCESS_A"
status "it cannot last more than a year" 422
call PUT "/groups/$GROUP/challenge" "{\"start\":\"$YESTERDAY\",\"end\":\"$TODAY\"}" "$ACCESS_A"
status "the admin sets one" 200
json "  the group has it" '.challenge_start' "$YESTERDAY"
call GET "$BOARD?period=challenge" "" "$ACCESS_A"
status "the challenge board" 200
json "  covers exactly those dates" '.from + " " + .to' "$YESTERDAY $TODAY"
json "  and adds up both days for Alice" '.me.steps' 21000
call DELETE "/groups/$GROUP/challenge" "" "$ACCESS_A"
status "the admin clears it" 200
json "  it is gone, and the group carries on" '.challenge_start' null

# ---------------------------------------------------------------------------
section "14. Group chat, moderation, reports"
MESSAGES="/groups/$GROUP/messages"
call POST "$MESSAGES" '{"body":"Hello team"}' "$ACCESS_B"
status "Bob posts in the chat" 201
MESSAGE_B=$(get .id)
json "  with his name" '.name' "Bob Test"
call POST "$MESSAGES" '{"body":"   "}' "$ACCESS_B"
status "an empty message is rejected" 422
call POST "$MESSAGES" '{"body":"let me in"}' "$ACCESS_C"
status "an outsider cannot post" 404
call GET "$MESSAGES" "" "$ACCESS_A"
status "Alice reads the chat" 200
json "  she sees Bob's message" '.items[0].body' "Hello team"
json "  and it is not marked as hers" '.items[0].mine' false
call POST "$MESSAGES" '{"body":"Nice one"}' "$ACCESS_A"
MESSAGE_A=$(get .id)
call DELETE "$MESSAGES/$MESSAGE_A" "" "$ACCESS_B"
status "Bob cannot remove someone else's message" 403
call DELETE "$MESSAGES/$MESSAGE_B" "" "$ACCESS_A"
status "an admin can" 204
call GET "$MESSAGES" "" "$ACCESS_B"
json "  the text is really gone" '.items[0].body' ""
json "  and it shows as removed" '.items[0].removed' true

call PATCH "/groups/$GROUP" '{"chat_enabled":false}' "$ACCESS_A"
call POST "$MESSAGES" '{"body":"hello?"}' "$ACCESS_B"
status "with chat turned off, nobody can post" 403
json "  says why" '.code' chat_disabled
call GET "$MESSAGES" "" "$ACCESS_B"
json "  and the old messages are not shown" '.items | length' 0
call PATCH "/groups/$GROUP" '{"chat_enabled":true}' "$ACCESS_A"

call POST "/friends/$ID_B/hide" "" "$ACCESS_A"
call GET "$BOARD?period=day&date=$TODAY" "" "$ACCESS_A"
json "hiding also applies inside groups: the board" '.total' 1
call GET "/groups/$GROUP/members" "" "$ACCESS_A"
json "  the member list" '.total' 1
call GET "$MESSAGES" "" "$ACCESS_B"
json "  and the chat" "[.items[] | select(.user_id == \"$ID_A\")] | length" 0
call DELETE "/friends/$ID_B/hide" "" "$ACCESS_A"

call POST /nudges "{\"to_user_id\":\"$ID_A\",\"kind\":\"taunt\",\"group_id\":\"$GROUP\"}" "$ACCESS_C"
status "an outsider cannot use a group to reach someone" 404
call POST /groups/join "{\"code\":\"$GROUP_CODE\"}" "$ACCESS_C"
status "Carol joins" 201
call PATCH "/groups/$GROUP" '{"taunts_enabled":false}' "$ACCESS_A"
call POST /nudges "{\"to_user_id\":\"$ID_A\",\"kind\":\"taunt\",\"group_id\":\"$GROUP\"}" "$ACCESS_C"
status "with taunts off, a taunt is refused" 403
call POST /nudges "{\"to_user_id\":\"$ID_A\",\"kind\":\"cheer\",\"group_id\":\"$GROUP\"}" "$ACCESS_C"
status "  but a cheer is fine, even without being friends" 201
call PATCH "/groups/$GROUP" '{"taunts_enabled":true}' "$ACCESS_A"

call GET "/groups/$GROUP/members" "" "$ACCESS_A"
json "the member list" '.total' 3
json "  admins come first" '.items[0].role' admin
call PATCH "/groups/$GROUP/members/$ID_B" '{"role":"owner"}' "$ACCESS_A"
status "an unknown role is rejected" 422
call PATCH "/groups/$GROUP/members/$ID_B" '{"role":"admin"}' "$ACCESS_C"
status "a plain member cannot promote anyone" 403
call PATCH "/groups/$GROUP/members/$ID_B" '{"role":"admin"}' "$ACCESS_A"
status "the admin promotes Bob" 204
call PATCH "/groups/$GROUP/members/$ID_B" '{"role":"member"}' "$ACCESS_A"
status "  and takes it back" 204
call PATCH "/groups/$GROUP/members/$ID_A" '{"role":"member"}' "$ACCESS_A"
status "the last admin cannot step down" 409

call GET "/groups/$GROUP/report.csv?from=$TODAY&to=$TODAY" "" "$ACCESS_C"
status "a plain member cannot download the report" 403
call GET "/groups/$GROUP/report.csv?from=$TODAY&to=$TODAY" "" "$ACCESS_A"
status "the admin can" 200
if head -1 "$BODY_FILE" | grep -q '^rank,name,total_steps,'; then ok "  it is CSV with a header"; else bad "report header: $(head -1 "$BODY_FILE")"; fi
if grep -q '^1,Bob Test,20000,' "$BODY_FILE"; then ok "  with the right totals"; else bad "report rows: $(cat "$BODY_FILE")"; fi
if grep -qi '^content-disposition: attachment' "$HEADERS_FILE"; then ok "  and downloads as a file"; else bad "no download header"; fi
call GET "/groups/$GROUP/report.csv?from=$TODAY&to=$YESTERDAY" "" "$ACCESS_A"
status "a backwards date range is rejected" 422

call PATCH "/groups/$GROUP" '{"mystery_mode":true}' "$ACCESS_A"
call GET "/groups/$GROUP/report.csv?from=$TODAY&to=$TODAY" "" "$ACCESS_A"
if grep -q 'Bob Test,0,' "$BODY_FILE"; then ok "in mystery mode the report cannot be used to peek at hidden steps"; else bad "report leaked hidden steps: $(cat "$BODY_FILE")"; fi
call PATCH "/groups/$GROUP" '{"mystery_mode":false}' "$ACCESS_A"

call DELETE "/groups/$GROUP/members/$ID_C" "" "$ACCESS_B"
status "a plain member cannot remove anyone" 403
call DELETE "/groups/$GROUP/members/$ID_A" "" "$ACCESS_A"
status "an admin uses leave, not remove, for themselves" 422
call DELETE "/groups/$GROUP/members/$ID_C" "" "$ACCESS_A"
status "the admin removes Carol" 204
call GET "/groups/$GROUP" "" "$ACCESS_C"
status "  she can no longer see the group" 404
call POST /groups/join "{\"code\":\"$GROUP_CODE\"}" "$ACCESS_C"
status "  and the invite link will not let her back" 403

call POST /groups '{"name":"Short lived"}' "$ACCESS_A"
TEMP_GROUP=$(get .id); TEMP_CODE=$(get .invite_code)
call POST /groups/join "{\"code\":\"$TEMP_CODE\"}" "$ACCESS_B"
call POST "/groups/$TEMP_GROUP/leave" "" "$ACCESS_A"
status "the only admin leaves" 204
call GET "/groups/$TEMP_GROUP" "" "$ACCESS_B"
json "  the longest-standing member takes over" '.my_role' admin
call POST "/groups/$TEMP_GROUP/leave" "" "$ACCESS_B"
status "the last person leaves" 204
call GET "/groups/$TEMP_GROUP" "" "$ACCESS_B"
status "  and the empty group is gone" 404

call POST /groups '{"name":"To be deleted"}' "$ACCESS_A"
TEMP_GROUP=$(get .id); TEMP_CODE=$(get .invite_code)
call POST /groups/join "{\"code\":\"$TEMP_CODE\"}" "$ACCESS_B"
call DELETE "/groups/$TEMP_GROUP" "" "$ACCESS_B"
status "a plain member cannot delete a group" 403
call DELETE "/groups/$TEMP_GROUP" "" "$ACCESS_A"
status "an admin can" 204
call GET "/groups/$TEMP_GROUP" "" "$ACCESS_B"
status "  it is gone for everyone" 404

# ---------------------------------------------------------------------------
section "15. Deleting an account (gateway coordinates every service)"
# A name starting with = would run as a formula if a spreadsheet took it literally.
call POST /auth/register "{\"name\":\"=Cmd()\",\"email\":\"$EMAIL_D\",\"password\":\"$PASSWORD\"}"
status "register Dave" 201
ACCESS_D=$(get .access_token); ID_D=$(get .user.id)
call POST /steps/sync "{\"source\":\"phone\",\"days\":[{\"date\":\"$TODAY\",\"steps\":5000}]}" "$ACCESS_D"
call POST /friends/accept "{\"code\":\"$INVITE_A\"}" "$ACCESS_D"
call POST /groups/join "{\"code\":\"$GROUP_CODE\"}" "$ACCESS_D"
call POST /groups '{"name":"Daves group"}' "$ACCESS_D"
DAVE_GROUP=$(get .id); DAVE_CODE=$(get .invite_code)
call POST /groups/join "{\"code\":\"$DAVE_CODE\"}" "$ACCESS_B"
call POST "/groups/$GROUP/messages" '{"body":"hi from Dave"}' "$ACCESS_D"
call POST /nudges "{\"to_user_id\":\"$ID_A\",\"kind\":\"cheer\"}" "$ACCESS_D"

call GET "/groups/$GROUP/report.csv?from=$TODAY&to=$TODAY" "" "$ACCESS_A"
if grep -q "'=Cmd()" "$BODY_FILE"; then ok "a formula in someone's name cannot run from the report"; else bad "the formula was not neutralized: $(cat "$BODY_FILE")"; fi
call GET "$BOARD?period=day&date=$TODAY" "" "$ACCESS_A"
json "before: Dave is on the board" '.total' 3

call POST /account/delete '{"password":"not-my-password"}' "$ACCESS_D"
status "the wrong password deletes nothing" 401
call POST /account/delete '{}' "$ACCESS_D"
status "the password is required" 422
call POST /account/delete "{\"password\":\"$PASSWORD\"}"
status "you must be signed in" 401
call POST /auth/login "{\"email\":\"$EMAIL_D\",\"password\":\"$PASSWORD\"}"
status "  Dave can still sign in" 200

call POST /account/delete "{\"password\":\"$PASSWORD\"}" "$ACCESS_D"
status "the right password deletes the account" 204
call POST /auth/login "{\"email\":\"$EMAIL_D\",\"password\":\"$PASSWORD\"}"
status "  Dave can no longer sign in" 401
call GET "$BOARD?period=day&date=$TODAY" "" "$ACCESS_A"
json "  he is off the group board" '.total' 2
call GET "/friends?date=$TODAY" "" "$ACCESS_A"
json "  off the friends board" "[.items[] | select(.user_id == \"$ID_D\")] | length" 0
call GET "/groups/$GROUP/messages" "" "$ACCESS_A"
json "  his chat messages are gone" "[.items[] | select(.user_id == \"$ID_D\")] | length" 0
call GET /nudges "" "$ACCESS_A"
json "  and so is his cheer" "[.items[] | select(.from_user_id == \"$ID_D\")] | length" 0
call GET "/groups/$DAVE_GROUP" "" "$ACCESS_B"
json "  the group he ran carries on under Bob" '.my_role' admin

# The services' /internal routes trust whoever calls them. curl would normally tidy
# up "..", so --path-as-is sends these paths exactly as an attacker would.
try_path() { curl --path-as-is -sS -o /dev/null -w '%{http_code}' -X "$1" -H "Authorization: Bearer $ACCESS_A" "$GATEWAY$2" 2>&1; }
STATUS=$(try_path DELETE "/internal/users/$ID_B")
status "the gateway does not expose /internal, even with a valid token" 404
STATUS=$(try_path DELETE "/api/v1/internal/users/$ID_B")
status "  not under /api/v1 either" 404
STATUS=$(try_path DELETE "/api/v1/workouts/../../../internal/users/$ID_B")
status "  not by climbing out of a public path" 404
STATUS=$(try_path DELETE "/api/v1/steps/%2e%2e/%2e%2e/%2e%2e/internal/users/$ID_B")
status "  not when the climbing is URL-encoded" 404
call POST /auth/login "{\"email\":\"$EMAIL_B\",\"password\":\"$PASSWORD\"}"
status "  and Bob's account is still there" 200

# ---------------------------------------------------------------------------
section "16. Cleanup and isolation"
call DELETE "/workouts/$WORKOUT_ID" "" "$ACCESS_A"
status "Alice deletes her workout" 204
call GET "/workouts/$WORKOUT_ID" "" "$ACCESS_A"
status "it is gone" 404
call DELETE "/diary/$ENTRY_ID" "" "$ACCESS_A"
status "Alice deletes her diary entry" 204

# Under docker compose the services publish no ports, so only the gateway can reach
# them. If you run the services directly on your machine (go run) they listen on
# localhost and this check cannot pass: set CHECK_PORT_ISOLATION=0.
if [ "${CHECK_PORT_ISOLATION:-1}" = "1" ]; then
  for port in 8081 8082 8083 8084 8085; do
    if curl -s --max-time 2 -o /dev/null "http://localhost:$port/health" 2>/dev/null; then
      bad "port $port is reachable from outside; services must only be reachable through the gateway"
    else
      ok "service port $port is NOT reachable directly (only the gateway is)"
    fi
  done
else
  echo "  (skipped: port isolation check, CHECK_PORT_ISOLATION=0)"
fi

# ---------------------------------------------------------------------------
echo
if [ "$failed" -eq 0 ]; then
  echo "${GREEN}${BOLD}All $passed checks passed.${RESET}"
else
  echo "${RED}${BOLD}$failed failed${RESET}, $passed passed."
  exit 1
fi
