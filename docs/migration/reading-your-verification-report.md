# Reading your verification report

**Audience:** officer. **When:** after every dry run, and again after you commit.

The verification report is the reason you can trust the import. It is produced identically by the dry
run and by the commit, it is stored permanently, and **every number in it links to the rows behind
it.** If a number looks wrong, click it.

The report is **publicly linkable and read-only**. Post the link in your guild Discord. Members can
check their own row without an account, which turns "did they lose my points" into something anybody
can answer for themselves in ten seconds.

---

## The header

One line, one colour:

> **Balances reconcile for 78 of 81 members across 2 pools. 3 members require attention.**

That is the whole report in a sentence. Below it: which install you imported (prefix and version),
how it was read (backup zip, dump file, or live connection), the importer version, and how long it
took.

**Green header, and §3 shows no red rows** — you can commit.
**Anything red** — do not commit yet. Read §3 first.

---

## Section by section

### §1 Row parity

For every table in your old database: how many rows were there, how many were read, how many were
imported, how many were skipped and why.

**Check:** the source row counts look like your guild. If your raids table says 40 and you have been
raiding for eight years, your backup is truncated — get a fresh one before doing anything else.

Also here: **every non-core table found, with row counts.** That is your plugin data, listed so you
know exactly what you are leaving behind.

### §2 Roster

Characters imported, people created, alt groups formed, merges applied automatically, merges queued
for you, duplicate names, characters whose class we could not identify.

**Check:** the alt-group count is roughly what you expect. If your guild has 80 characters across 45
people and this says 80 people, your alt setting is probably wrong — go back and check the
"do alt points roll into the main" answer.

**Queued merges** are not errors. They are two characters with the same name that both have history,
so we imported both rather than guessing. You resolve them afterwards, with both histories side by
side.

### §3 Per-member balance delta — the one that matters

One row per member per pool:

| Member | Pool | Old site showed | Source | We computed | Reconciliation | Final | Δ | Classification |
|---|---|---|---|---|---|---|---|---|
| Zibaxia | Main | 1,699.50 | live API | 1,842.00 | −142.50 | 1,699.50 | 0 | `apa_decay` |
| Grumbok | Main | 214.00 | cache | 226.00 | −12.00 | 214.00 | 0 | `orphan_rows` |
| Nilbog | Main | 88.00 | cache | 61.00 | — | 61.00 | **−27.00** | **`unexplained`** |

Sorted by size of difference. Downloadable as CSV so you can open it in a spreadsheet next to your
old standings page.

**Two headline numbers:**

- **`Σ|Δ final|` must be 0** in the default mode. Every member ends on the number their old site
  showed. If it is not 0, something is wrong and the commit is blocked.
- **`Σ|Δ before reconciliation|`** is how far the raw facts were from the old display, with a
  breakdown by cause. This number being large is *normal* for a guild that used decay — it is exactly
  the decay.

**Any row classified `unexplained` renders red and blocks the commit.** That is a feature. It means
we cannot account for the difference, and shipping a number we cannot explain is worse than stopping.

### §4 Totals

Per pool: total earned, spent and adjusted — old site vs ours, in absolute and percentage terms.

**Why it is separate from §3:** a total that matches while individual members do not is the signature
of an attribution bug — points landing on the wrong person. Check both levels, not just the total.

### §5 Raids and attendance

Raid sessions, ticks, connected-attendance groups, raids per pool and per event, distinct raid days,
and the full date range of your history.

Then the **attendance spot-check**: for your top 20 raiders, their 30/60/90-day and lifetime
attendance percentages on the old site next to ours.

**Check this properly.** Attendance percentage is the second number your guild will look at, right
after their own balance, and it is what gates loot in most guilds. A consistent small difference
across everyone usually means an event-to-pool mapping flag was misread — bring it to an issue with
this section attached.

### §6 Items and loot

How many awards were imported, and how many distinct item names resolved exactly, by alias, by a
match you confirmed, or not at all.

The **unmatched item list** is sorted by total DKP spent, not alphabetically, because that is the
order in which fixing them matters. Each entry shows the name, how many times it was bought, when it
was first and last seen, and our suggested matches.

**No award is ever dropped because we could not identify the item.** The buyer, price, raid and date
are all correct; only the catalogue link is missing, and you can fix those from the reconciliation
queue at your leisure.

### §7 Adjustments

Count, groups preserved, and — importantly — **adjustments that pointed at no pool** in EQdkp, with a
count and a total value.

These are real adjustments that your old site displayed nowhere. Importing them creates a difference
on purpose, and the report tells you which pool they landed in.

### §8 Skipped rows

Every skipped row, with a reason code and its ID in your old database. Counts on screen, full list
downloadable.

**The most common entry is normal:** attendance rows pointing at characters that were deleted from
`members` years ago. EQdkp had no foreign key there, so these accumulate. They show up here, they
explain a chunk of §3, and there is nothing to fix.

**Nothing is skipped without appearing in this section.** That is the guarantee this whole document
rests on.

### §9 Accounts

Users imported, how many have a usable email, how many share an email with another account, how many
are disabled, how many have no characters, how many claim codes were generated, and which claim
channels are actually available to you (is Discord configured? is email verified end to end?).

### §10 Permissions

The full downgrade table, plus two lists you should read out loud to yourself:

- **everyone whose capability shrank, naming the specific capability** — "Grumbok can no longer
  delete raids";
- **everyone who was an EQdkp super-admin**, to promote once they have claimed their account.

### §11 Encoding

How many text values were repaired, with a 20-row before/after sample.

**Look at the sample.** Twenty rows takes thirty seconds and it is the fastest way to confirm that
`GrÃ¼ÃŸe` became `Grüße` and that `Trakanon&#39;s Tooth` became `Trakanon's Tooth`. If the "after"
column looks worse than the "before" column, stop and file an issue.

Also here: values that MySQL truncated in your *original* install years ago. Those characters are
gone and cannot be recovered by anyone — we can only tell you it happened.

### §12 Assets and content

Images found, copied, deduplicated, rejected (with a reason) and unresolved. Then, for articles: which
HTML tags were stripped, which links were rewritten, and a "ten largest articles, before and after"
sample.

Read the article sample if news matters to your guild. We sanitise on import, because EQdkp stored
raw markup and rendered it at display time, which is a documented cross-site-scripting surface.

### §13 Precision

Old DKP values were decimals; here they are whole hundredths of a point stored as integers, so no
value can drift. This section reports any row where converting and converting back was not exact,
with the maximum and total drift. It should be zero or negligible.

### §14 Ledger integrity

This section checks the guardrails, not the data:

- the batch hash chain is intact;
- the sum of all ledger entries equals the sum of all balances;
- a balance snapshot rebuilt from zero matches the running one;
- **the append-only protection actually fires** when something tries to modify history.

This is the answer to "how do I know you did not just write the numbers you wanted to show me". All
four must pass.

### §15 Determinism

Three hashes. Re-running the same import with the same settings reproduces this exact report. If you
re-run and get a different one, that difference is itself information.

---

## What a legacy reconciliation line means

If your guild used decay, caps or start points, your members' balances were **not** a function of
your EQdkp database. They were the raw totals with rules applied on top, and those rules live in a
PHP file on your web server that a database backup does not contain.

So the import does this instead:

1. Rebuild every raid, item and adjustment exactly. That is the true history.
2. Work out what your old site actually displayed, using the best available source (see below).
3. Post **one visible line per member** for the difference, labelled and explained.

```
Legacy reconciliation — EQdkp point decay (APA #3, ~10%/30d).
Imported history totals 1,842.00; EQdkp displayed 1,699.50.        −142.50
```

That line is a normal ledger entry. It appears in the member's statement, dated the cutover moment,
with a reason. It is not hidden, not folded into an opening balance, and can be reversed like
anything else. **It is the only honest way to have both a real history and a matching balance.**

**Where the "old site showed" number comes from**, best first:

| Source | Quality |
|---|---|
| A live connection to your old API (`--oracle-url`) | Exact. It is literally the number your members saw |
| EQdkp's per-rule results cache | Exact total, plus which rule caused what |
| EQdkp's periodic point snapshots | Good, up to a few weeks stale |
| EQdkp's simple totals cache | Validates our arithmetic but cannot explain decay |

The rung used is shown per member in §3. If most of your rows say "simple totals cache" and you have
decay, consider giving the wizard a read-only API token so it can use rung 1.

**If you would rather not have residual lines at all**, there are two other modes, chosen in step 3
of the wizard:

| Mode | What you get |
|---|---|
| **Opening balance** | Full raid, loot and attendance history as records, but one clean "opening balance" per member instead of a per-transaction ledger. Right for guilds with heavy decay, where a per-transaction ledger would be a fiction |
| **Facts only** | The real history, no residual. Balances differ from your old site by exactly the decay amount, and you re-enter your rules and let the new engine own the truth. Clean-slate option; the report still shows every member's exact difference so nobody is surprised |

---

## A member says their number is wrong

Work through this in order. Steps 1–3 cover almost everything.

1. **Open their statement.** Every raid, item and adjustment, with a running balance. Nine times out
   of ten the disagreement is with a specific raid or item, not with the migration, and now you can
   both see it.
2. **Check their classification in §3.** If it is `apa_decay`, `orphan_rows`, `stale_cache`,
   `unattributed_adjustment` or `float_rounding`, the difference has a known cause:

   | Label | What to tell them |
   |---|---|
   | `apa_decay`, `apa_cap`, `apa_start_points` | "Our history is complete; the decay your old site applied is the labelled line at the bottom." |
   | `orphan_rows` | "N of your old raid credits pointed at a character that had been deleted. Here is the list." |
   | `stale_cache` | "Your old site's cached number was out of date. **Our number is the correct one.**" |
   | `unattributed_adjustment` | "You had adjustments that EQdkp attributed to no pool and displayed nowhere. They are now in `<pool>`." |
   | `float_rounding` | A fraction of a point. Cosmetic |
   | `twink_mode_mismatch` | Your alt setting is wrong. Go back to step 3 of the wizard and re-run the dry run |

3. **Check §8 for their name.** Skipped rows are listed with the ID they had in your old database, so
   you can look them up there if you want to confirm.
4. **If the label is `unexplained`**, do not talk yourself into a theory. That is a bug in the
   importer until proven otherwise — see below.

Nothing you do here is destructive. If you decide the import was wrong, undo it and re-run.

---

## When to stop and file a bug

File an issue if any of these are true. Attach the **JSON** version of your report — it contains
counts and structure, not personal data — and say which fixture-shaped thing your install is (EQdkp
version, prefix, roughly how many members and raids).

- **Any row in §3 is `unexplained`.** This is the important one. The set of members whose totals
  differ is supposed to be exactly the set predicted by decay detection, skipped rows and
  unattributed adjustments — that equality is asserted in our test suite against real EQdkp
  installs. A row outside it means our prediction is wrong.
- **§14 does not fully pass.**
- **The commit report differs from the dry-run report.** They are generated by the same code from the
  same data and should be identical.
- **§5 attendance percentages are consistently off** for everyone by the same few points.
- **The §11 encoding sample looks worse after than before.**

An import you had to stop is a much better outcome than an import you had to unwind three weeks
later, and a report that names its own uncertainty is doing exactly what it was built to do.
