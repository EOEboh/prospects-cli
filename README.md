# prospect

A CLI that turns "recruiting agencies in Austin" into a scored, deduplicated,
resumable list of prospects with contact details and the reasoning behind each
score.

The score breakdown is the point. A score of 85 with no explanation can't open
a cold email; every score here carries the plain-English reason it landed
where it did.

**Status: phase 1 of 8.** The command surface, configuration, schema and
migrations are in place. Command bodies land phase by phase and currently exit
with `not implemented yet (phase N)`.

| Phase | Delivers | State |
|------:|----------|-------|
| 1 | Skeleton, config, SQLite + migrations, logging, quota ceilings, `quota` | done |
| 2 | `seed` — CSV import and dedup | done |
| 3 | `enrich` — fetch, robots.txt, extraction, cache, rate limit; `signal` | done |
| 4 | `score` — YAML weights and explanations | done |
| 5 | `list`, `export`, `status`, `suppress`, `brief` | done |
| 6 | `discover` — Google Places, field masking, dry-run, quota ceiling | done |
| 7 | Meta Ad Library as an optional flag-gated source | pending |
| 8 | Tests, docs, example weights config | pending |

**The tool is fully useful now**, with no API keys and no billing enabled
anywhere. Phases 6 and 7 add optional paid discovery; they are an upgrade, not
a dependency.

## Build

Requires Go 1.25 or newer (`modernc.org/sqlite` sets the floor). CGo-free, so
the binary deploys to a fresh VPS with no system SQLite.

```sh
git clone <this repo> && cd prospect
cp .env.example .env      # edit PROSPECT_USER_AGENT_EMAIL
go build -o prospect ./cmd/prospect
./prospect --help
```

The database is created and migrated on first run.

## Zero-key quickstart

No API keys, no billing, no Cloud Console.

```sh
# 1. Hand it a CSV. Header row required; name is the only mandatory column.
cat > businesses.csv <<'CSV'
name,website,city
Acme Recruiting,https://acmerecruiting.com,Austin
Bright Path Talent,https://brightpathtalent.com,Austin
CSV
./prospect seed --csv businesses.csv --dry-run   # see what it would do
./prospect seed --csv businesses.csv

# 2. Fetch their sites and record what they reveal about lead handling.
./prospect enrich --all-pending

# 3. Score from whatever signals exist. Partial data still scores.
./prospect score

# 4. Read the morning brief.
./prospect brief
```

### The CSV format

A header row is required. Column order is irrelevant, unknown columns are
ignored, and header names match loosely — `Business Name`, `business_name` and
`name` are the same column. Excel's UTF-8 BOM is stripped.

| Column | Aliases | Notes |
|--------|---------|-------|
| `name` | `business name`, `company` | Required. A row with only a website is accepted; the URL stands in. |
| `website` | `url`, `site`, `homepage` | Optional but strongly recommended — it is the dedup key. |
| `city` | `town`, `locality` | Scopes the fallback dedup key when `website` is absent. |
| `email`, `phone`, `address`, `region`, `country` | `state`, `street`, … | Optional. |

One malformed row is reported and skipped; it never fails the import.

### How dedup works

Businesses are matched on **normalized website domain** first, falling back to
**normalized name plus city**. Both keys are computed in one place and mirrored
by unique indexes, so the application and the database never disagree.

- `acme.com`, `www.acme.com`, `https://acme.com/contact` and
  `careers.acme.com` are all one business.
- `acme.wixsite.com` and `bright.wixsite.com` are **two** businesses. Website
  builders put every customer on one registrable domain, so the subdomain is
  the identity — folding these together would silently delete exactly the kind
  of prospect this tool exists to find.
- `facebook.com/acme` and `facebook.com/bright` are likewise two businesses:
  on those platforms the path is the identity, not the host.
- `Acme Recruiting`, `Acme Recruiting, Inc.` and `The Acme Recruiting Co.` in
  the same city are one business. The same name in a different city is two —
  business names are only unique locally.

Re-importing an updated file merges new details in without overwriting
anything with a blank, and **re-importing an unchanged file is a complete
no-op** — nothing is written and `updated_at` does not move.

Suppressed domains are never recreated, so a business that asked to be removed
stays gone even if it is still sitting in your spreadsheet.

## Daily workflow

```sh
./prospect brief                    # the morning read: top 10 uncontacted
```

```
Morning brief — top 3 uncontacted prospect(s), 2 still need an ad check
────────────────────────────────────────────────────────────────────────────

 1. Acme Recruiting                           77/100
    hello@acmerecruiting.com  ·  https://acmerecruiting.com

    Why:  Acme Recruiting scores 77/100. It is currently running paid ads
          (checked ad library), has a contact form with no CRM or scheduling
          tool behind it (https://acmerecruiting.com/enquiry), promises a
          response time of 24 hours or more (We respond within 24 hours.),
          and lists a public contact email.

    Open: You're running ads at the moment — worth knowing what happens to
          those enquiries after they land.
────────────────────────────────────────────────────────────────────────────

 2. Cedar Staffing                            41/100
    info@cedarstaffing.com  ·  https://cedarstaffing.com

    Why:  ... Confidence is low (41% of available signals checked), so treat
          this as provisional.

    Open: Your contact form posts straight to an inbox — there's no CRM or
          scheduler picking those up.

    ! Ad check pending — the strongest signal is still unknown.
      prospect signal 3 --type running_ads --value true|false
```

The **Open:** line is the suggested first sentence of the cold email, drawn
from whichever signal contributed most. It is read from `weights.yaml` at print
time, so rewording it takes effect immediately without rescoring.

The rest of the loop:

```sh
# Check the flagged prospects at the Ad Library, record what you find:
./prospect signal 3 --type running_ads --value true --note "checked ad library"
./prospect score                    # rescore; 41 → 77 if they are advertising

./prospect status 1 --set emailed_1 --note "sent form observation angle"
./prospect status 1                 # current status plus the full history
./prospect list --min-score 60 --status not_contacted
./prospect export --format csv --min-score 60 --out prospects.csv
./prospect suppress 2 --reason "asked to be removed"
```

`prospect signal --help` lists the full signal vocabulary.

### Outreach tracking

Statuses run `not_contacted → emailed_1 → emailed_2 → emailed_3 → replied →
call_booked → won`, with `dead` available at any point. Transitions are
appended to a history table rather than overwriting, so "emailed twice, no
reply" stays distinguishable from "emailed once last week":

```
$ prospect status 1
Acme Recruiting (#1)
  status: replied
  note:   asked for pricing
  email:  hello@acmerecruiting.com

History:
  2026-07-29 18:29  not_contacted → emailed_1  sent form observation angle
  2026-07-29 18:29  emailed_1 → emailed_2      followed up
  2026-07-29 18:29  emailed_2 → replied        asked for pricing
```

### Suppression is permanent

`prospect suppress` excludes a business from every list, export, brief and
enrichment — and suppresses its **domain**, so re-importing the same CSV or
re-running `discover` will not recreate it:

```
$ prospect suppress 2 --reason "asked to be removed"
Suppressed Globex Corp (#2): asked to be removed
The domain globex.com is suppressed too, so it will not be recreated.

$ prospect seed --csv businesses.csv
  0 new
  1 skipped (suppressed)
```

Moving a suppressed business back into the pipeline is refused. Its outreach
entry is closed with the reason, so the history explains why it stopped rather
than showing it simply going quiet.

### Export

Both formats carry the explanation and the component breakdown, so the
reasoning travels with the data — a row pasted into a spreadsheet still says
why it is there. CSV flattens the breakdown into a `top_signals` column; JSON
keeps it structured.

## Optional API keys

Everything above works without these.

### Google Places — `discover`

Enables discovery by niche and location instead of hand-built CSVs.

1. Create a project at <https://console.cloud.google.com/>.
2. Enable the **Places API (New)**.
3. Create an API key, restrict it to the Places API, and set
   `GOOGLE_PLACES_API_KEY` in `.env`.

**This tool cannot spend money by default.** `PROSPECT_ALLOW_PAID_APIS` is
`false`, and while it is false no ceiling may exceed the free monthly
allowance — a large `PROSPECT_PLACES_MONTHLY_MAX` is clamped, not honored.
Going past the free tier takes a deliberate edit to that one switch.

#### What "free" actually means here

Google's free allowances are **per SKU per calendar month**, and the SKU is
decided by the field mask: **billing is at the highest tier among the fields
requested.**

| Tier | Free calls / SKU / month |
|------|-------------------------:|
| Essentials | 10,000 |
| Pro | 5,000 |
| Enterprise | 1,000 |

`places.websiteUri` is an **Enterprise** field. It is also this tool's dedup
key and the page `enrich` fetches, so discovery cannot avoid it — **`discover`
bills at Text Search Enterprise, with 1,000 free calls per month.** That is the
number to plan around, not 10,000.

The upside: `rating` and `userRatingCount` are in that same Enterprise tier, so
the size heuristic rides along at no extra cost. They are always requested.

With the default 0.9 safety margin, the effective ceiling is **900 discover
calls per month**. At up to 20 results per call, that is roughly 18,000
businesses a month — far more than a single operator can work through.

#### The three layers, which are not interchangeable

- **In this tool**: the ceiling is checked before every billable call and hard
  stops the run. `prospect quota` shows where you stand.
- **In the Cloud Console**: set per-API quotas *below* the free allowance under
  *APIs & Services → Places API → Quotas*. This is the only layer that stops
  spend caused by something other than this tool — a leaked key, another
  machine, a different app on the same project.
- **Budget alerts are not a limit.** They email you after the fact and do not
  stop usage. Set one anyway; do not rely on it.

The safety margin exists because the local counter can drift below Google's:
retried requests, another machine sharing the key, or other usage on the same
project. Stopping at 90% absorbs that drift.

Other cost discipline:

- Responses are cached for 7 days by default. Reruns do not re-fetch, and cache
  hits are recorded as non-billable so `prospect quota` shows what the cache
  saved. The ceiling is checked *after* the cache lookup, so a cached page is
  neither counted nor refused.
- Unrecognized field names resolve to the Enterprise tier rather than
  optimistically cheap, so a future Google field cannot quietly slip past the
  ceiling.
- Paging stops as soon as `--limit` is met, and never exceeds 3 pages.
- An error response is never cached — one transient failure would otherwise
  become a week of them.

#### Check the cost before you spend it

`--dry-run` reports what a run would cost and where the ceiling currently sits,
without making a call:

```
$ prospect discover --niche "recruiting agency" --location "Austin, TX" --limit 50 --dry-run

Dry run: "recruiting agency in Austin, TX"

  billed as     text_search_enterprise (enterprise tier)
  tier set by   places.websiteUri
  worst case    3 billable call(s) for up to 50 result(s)
  used in 2026-07  0 of 900
  remaining     900

This fits inside the ceiling.
```

Worst case, not exact: a paged API only reveals how many pages exist by being
called, cached pages cost nothing, and a query with few results stops early.

When the allowance is gone, the run stops **before** the call rather than after:

```
$ prospect discover --niche "recruiting agency" --location "Austin, TX"
prospect: google_places/text_search_enterprise: 900 of 900 calls used in 2026-07
— stopping to stay inside the free tier (raise PROSPECT_PLACES_MONTHLY_MAX and
set PROSPECT_ALLOW_PAID_APIS=true to go past it)
```

#### Where discovery fits

`discover` creates business rows; it does not gather signals. The full paid path
is:

```sh
./prospect discover --niche "recruiting agency" --location "Austin, TX" --limit 50 --dry-run
./prospect discover --niche "recruiting agency" --location "Austin, TX" --limit 50
./prospect enrich --all-pending     # free: reads their own websites
./prospect score
./prospect brief
```

Discovered businesses are deduplicated against everything already stored on the
same domain and name keys as CSV imports, so running `discover` over a niche you
have already seeded adds only what is new. Suppressed domains are never
recreated.

### Meta Ad Library — optional, off by default

Whether a business runs ads is the strongest buying signal, but the public
API's coverage of non-EU commercial ads is unreliable, so nothing is
architected around it.

The primary path is manual: check the Ad Library web UI for the prospects at
the top of your list and record the result with `prospect signal`. Hand-entered
signals live in the same table as automated ones and score identically.
`prospect brief` tells you which prospects still need that check.

To enable the API source anyway, set `META_ADS_ACCESS_TOKEN` and
`PROSPECT_ENABLE_META_ADS=true`, then pass `--with-meta-ads` to `enrich`. The
Ad Library API is not billed per call, but it is rate limited, so a local cap
still applies to keep a runaway loop from earning a throttle.

## Scoring

Weights live in YAML, not in constants, so they can be tuned as you learn what
converts. Without a `weights.yaml` the built-in defaults apply, so `score`
works out of the box:

```sh
./prospect score --print-config > weights.yaml   # start from the defaults
./prospect score --explain                       # see every contribution
```

| Rule | Points |
|------|-------:|
| Currently running ads | 40 |
| Contact form with no automation behind it | 20 |
| Stated response time of 24 hours or more | 15 |
| Hiring for a data-entry or lead-management role | 15 |
| Size suggests 2–50 employees | 10 |
| Public contact email found | 10 |
| Enterprise tooling detected | −25 |
| Live chat widget present | −5 |

Some of these are conditions, not plain weights — "a contact form with nothing
behind it" is the whole point, and a form posting to a Salesforce web-to-lead
endpoint is the most automated form there is. So rules are small declarative
conditions rather than a flat weight map:

```yaml
- id: unautomated_contact_form
  points: 20
  when:
    all:
      - {signal: contact_form, equals: "true"}
      - {signal: automation_tag, absent: true}
      - {signal: enterprise_marker, absent: true}
  explain: has a contact form with no CRM or scheduling tool behind it
```

Conditions support `equals`, `one_of`, `at_least`, `at_most`, `present`,
`absent`, and `all`/`any`/`not`. Signal names are validated at load, so a typo
in `weights.yaml` fails immediately instead of silently never matching.

### The score is normalized

The printed score is **0–100**, computed against the sum of positive weights in
the active config. The raw sum, that maximum, and a hash of the config are all
stored with every score. Doubling every weight leaves the printed score
unchanged — so `--min-score 60` keeps meaning the same thing after a retune,
and scores from before and after stay distinguishable.

### Partial data still scores

A rule that cannot be evaluated contributes nothing and **lowers confidence**
rather than blocking the score. Confidence is coverage: the share of available
points that could actually be checked. A business whose ad status has never
been recorded is missing 40 of 110 points, so it caps out around 64%.

The explanation says so out loud rather than presenting a guess as a fact:

```
Acme Recruiting scores 41/100. It has a contact form with no CRM or scheduling
tool behind it (http://acmerecruiting.com/enquiry), promises a response time of
24 hours or more (We respond within 24 hours.), and lists a public contact
email (hello@acmerecruiting.com). Not yet checked: whether they are hiring for
lead handling, whether they run ads and company size. Confidence is low (41% of
available signals checked), so treat this as provisional.
```

Record the ad signal and the same business rescores at 77 with the flag
cleared. That gap between 41 and 77 is exactly why `brief` tells you who still
needs a manual ad check.

## What `enrich` reads

Up to three pages per business — the homepage plus up to two contact pages it
links to. If the homepage links to none, a couple of conventional paths
(`/contact`, `/contact-us`) are tried, and that is the end of the guessing.

| Signal | How it is read |
|--------|----------------|
| `contact_email` | `mailto:` links and page text, filtered against placeholders, `noreply@` and platform noise |
| `contact_form` | A form taking a message or an email; the resolved action endpoint is stored as evidence. Search boxes and newsletter-only signups do not count |
| `chat_widget` | Intercom, Drift, Tawk, Crisp, LiveChat, Zendesk, Tidio, Olark, Freshchat, HubSpot, Messenger |
| `automation_tag` | HubSpot, Calendly, Intercom, Mailchimp, Typeform, ActiveCampaign, Klaviyo, ConvertKit, Jotform, Gravity Forms, Zapier, Acuity, Pipedrive, Zoho |
| `enterprise_marker` | Salesforce/Pardot, Marketo, Workday, Greenhouse, Lever, Eloqua, Adobe Experience Cloud, SAP |
| `response_time_hours` | A stated turnaround in the page text, stored with the sentence that said it |

Absence is recorded as explicitly as presence — "no contact form" is a fact,
and it has to be distinguishable from "never looked".

Signals carry a confidence: **0.7** when only the homepage could be read,
**0.9** when a contact page confirmed them. Third-party tools are matched on
specific fingerprints (script hosts, inline globals), not on brand names in
prose — "we use Mailchimp" in a blog post is not a Mailchimp integration.

### Signal history

Signals are appended **when the value changes**, not on every run. Re-observing
the same value bumps `last_seen_at`; a different value supersedes the old row
and inserts a new one.

That is what makes the interesting question answerable. A business that was not
running ads last month and is running them today has just entered its buying
window, and `prospect signal` says so out loud when it happens:

```
$ prospect signal 42 --type running_ads --value true --note "checked ad library"
Recorded running_ads=true for Acme Recruiting (#42) — this CHANGED from the previous value.
```

## Crawling rules

Enforced in code, not left to discipline:

- `robots.txt` is honored on every fetch, parsed per RFC 9309 — wildcards,
  `$` anchors, longest-match precedence and per-agent groups. A disallowed page
  is skipped and the reason recorded as a `robots_disallowed` signal, so "not
  fetched" stays distinguishable from "nothing found".
- A site's `Crawl-delay` is obeyed when it is **slower** than the configured
  rate. A site asking to be crawled faster does not get its way.
- The User-Agent names the tool and `PROSPECT_USER_AGENT_EMAIL`, which is
  required. There is no anonymous fallback, and commands that fetch refuse to
  start without it.
- At most one request per second per host, enforced across the whole worker
  pool, so two workers on one host still queue behind each other. Every request
  has a timeout, a 4 MiB body cap and a redirect limit.
- Retries use exponential backoff with jitter on 429 and 5xx, and honor
  `Retry-After`. 4xx is an answer, not a failure, and is never retried.
- Contact forms are **detected, never submitted**. This tool issues `GET` and
  nothing else. Bulk submission is spam and would get your domain flagged.
- Only publicly listed business contact details are collected.
- Suppression is enforced by the database: `list`, `export` and `brief` read a
  view that already filters suppressed businesses, and suppression applies to
  the domain as well as the row, so a rediscovered business stays excluded.

## Configuration

Every knob is an environment variable, documented in `.env.example`. `.env` is
loaded for local development and never overrides a real environment variable.

## Layout

```
cmd/prospect/          entry point
internal/
  cli/                 one file per command
  config/              env + .env loading, validation
  dedup/               domain and name normalization — the identity rules
  logging/             slog wiring
  httpx/               the only network client: robots, cache, rate limit
  model/               domain types and the signal vocabulary
  quota/               SKU tiers, field-mask cost resolution, monthly ceilings
  scoring/             rules, weights, confidence, explanations
  store/               SQLite, migrations, repositories
    migrations/        versioned SQL, applied on startup, idempotent
  source/              the Source interface every data source implements
    csvseed/           the zero-API path
    website/           signal extraction from a business's own pages
    places/            optional paid discovery, behind the quota ceiling
```
