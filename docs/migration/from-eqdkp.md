# Move your guild from EQdkp Plus

**Audience:** officer. **Time:** about 40 minutes for the first dry run. **Risk to your old site:** none.

Read these three sentences first. They are repeated at the bottom of the page.

1. **The importer only ever reads.** It never writes to your EQdkp database, never logs into your old
   site as anyone, and never changes a single row there.
2. **A dry run is the default.** Nothing is written to Dragon Kill Party until you press **Commit**,
   and you can dry-run as many times as you like.
3. **You can undo a commit.** A snapshot is taken automatically immediately before the import writes
   anything, and there is an **Undo this import** button.

Ten years of raid history is exactly the thing this is built to protect. If anything on this page
does not match what you see, stop and ask before committing — an unread report is the only way this
goes wrong.

---

## Step 1 — Get your backup

**Recommended: the backup your EQdkp already makes.**

> EQdkp admin panel → **Maintenance** → **Backup** → **Download**

That gives you a `.zip`. It is a complete copy of your database, it names your table prefix in its
own header (so you do not need to know what your prefix is), and it is a file you should be keeping
anyway. This is the path to use unless someone has told you otherwise.

**If that button fails or times out**, your host's control panel almost certainly has "Export
database" (phpMyAdmin → Export → Go). A `.sql` or `.sql.gz` file works identically.

**If you have a technical guildmate and a server you can reach**, a live read-only database
connection is slightly better, because re-importing later takes one click instead of one more
backup. The import wizard prints the exact command to create a read-only user. This matters only if
you plan a [parallel run](parallel-run-and-cutover.md).

**Do not use the XML export.** ACP → Data-Export produces a *balances snapshot*, not a history. It
contains no per-raid attendance rows, so attendance percentages cannot be rebuilt from it. If it is
genuinely all you can get, the wizard will accept it and will tell you what you are losing.

| What you have | Use it? |
|---|---|
| ACP backup `.zip` | **Yes — best** |
| `mysqldump` / phpMyAdmin `.sql`, `.sql.gz` | **Yes — equally good** |
| Live MySQL credentials | Yes — best for a parallel run |
| ACP XML / JSON export | Only as a last resort. Balances, no history |
| A spreadsheet of standings | Works, but see the CSV and spreadsheet import instead |

### Your files and images

Avatars, rank icons and article images are not in the database — they are files on your web host.
If you want them, zip up the `files/` and `images/` folders from your EQdkp install and have that
zip ready too. If you skip this, everything still imports; the report just tells you how many image
references it could not resolve, and you can add the zip later.

---

## Step 2 — Start the import and read the analysis

Settings → **Import** → **From EQdkp Plus**, then drag your zip in.

The wizard fingerprints your install first and shows you what it found. Check these four things:

| What it shows | What you are checking |
|---|---|
| **Detected prefix and version** | That it matches your install. If your database contains two EQdkp installs, you get a list — pick yours by row count |
| **Row counts per table** | That the numbers look like your guild. If `raid_attendees` says 300 and you have 20,000 raid credits, your backup is truncated — get a fresh one |
| **Plugin tables found** | Data we cannot import, listed by name **with row counts**, so you know what you are leaving behind |
| **Decay / cap detected?** | If yes, read the box. Your decay rules are stored in a file on your web server, not in the database, so we cannot read them — see [what does not migrate](what-does-not-migrate.md) |

Then answer four questions. Each has a recommended default and one sentence explaining the
consequence; if you are unsure, take the default and check the answer in the report afterwards.

| Question | Default | What it decides |
|---|---|---|
| **Do alt points roll into the main?** | Whatever your EQdkp `show_twinks` setting says | Whether an alt group is one member with one balance, or separate balances per character |
| **How should balances be matched?** | Per-member adjustment | See [reading your report](reading-your-verification-report.md#what-a-legacy-reconciliation-line-means) |
| **Were your raid times stored in UTC?** | UTC | We show you a histogram of when your raids happened. If they cluster at 19:00–23:00 **UTC** and your guild raids in the evening **local** time, the honest answer might be "local" |
| **Which point system going forward?** | None — you pick | Imported history is frozen as history. Your forward rules start at cutover |

---

## Step 3 — Run the dry run and read the report

Press **Dry run**. Nothing is written. You get the [verification report](reading-your-verification-report.md),
which is the whole point of this exercise.

**Read at least these three sections before you commit:**

- **§3 Per-member balance delta** — one row per member per pool, showing what your old site displayed
  and what we computed. Sort by the difference column. Anything marked **unexplained** is red and
  blocks the commit.
- **§8 Skipped rows** — every row we could not import, with a reason and its ID in your old database.
  Attendance rows pointing at deleted characters are the usual entry here, and they are usually fine.
- **§5 Attendance spot-check** — your top 20 raiders' 30/60/90-day and lifetime attendance
  percentages, old site vs ours. This is the second number your guild will check, right after their
  own balance.

Send the report link to another officer and have them look at their own row. The report is publicly
linkable and read-only, so you can post it in Discord without giving anyone an account.

**Running the dry run three or four times is normal.** Fix an item alias, mark two duplicate
characters as the same person, change your alt setting, re-run. Nothing is committed until you say
so.

---

## Step 4 — Commit

When the report says what you expect, press **Commit**.

You will be asked to re-enter your password. That is deliberate: committing an import is one of a
small number of actions that require you to prove you are still you.

What happens, in order:

1. A snapshot of your Dragon Kill Party database is saved. This is the undo point.
2. The import runs as a background job with a live progress bar. You can close the tab.
3. A second verification report is produced and **diffed against the dry-run report**. If they differ
   at all, that is flagged — it should not happen, and if it does we want to know.

If something goes wrong mid-import, nothing is lost: the run resumes from where it stopped. If you
change your mind afterwards, **Undo this import** restores the snapshot exactly, and the button tells
you how many changes have happened since the import so you know what undoing would cost.

---

## Step 5 — Verify, then hand out accounts

**Verify.** Open your own member statement. It lists every raid, item and adjustment from the last
ten years with a running balance, ending on the number you saw on the old site. Do the same for two
or three members who will definitely check.

**Passwords do not come across. None of them, ever.** This is deliberate — see
[what does not migrate](what-does-not-migrate.md#passwords). Every imported account arrives unable to
log in until its owner claims it, in one of three ways:

| Channel | What the member does | What you do |
|---|---|---|
| **Discord** | Clicks "Log in with Discord" | Nothing, if their Discord email matches their old EQdkp email — it links automatically |
| **Claim code** | Enters a code like `H4KP-7TQR-9M`, then sets a password or links Discord | Download the claim-code list once, or copy the ready-formatted Discord block and paste it into your guild server |
| **You, per person** | Clicks a link you send them | Open their account, press "generate claim link", DM it |

Send every member their **statement link alongside their claim code**. Somebody who opens a page that
shows their own ten years of loot, correct to the point, does not argue about the migration.

**Officers are not promoted automatically.** Anyone who was an EQdkp super-admin is listed in report
§10 as "promote after they claim". An unclaimed account whose only credential is a code in a
spreadsheet must not be an admin, so you promote them by hand — one click — once they have logged in.

---

## Step 6 — Keep your bots running

Point Castle Steward, bidbot2, jDKP, froakbot or your home-grown script at:

```
https://<your-dkp-host>/api/compat/eqdkp/api.php
```

It speaks the protocol your bots already speak, including the `?atoken=` query parameter, and your
old numeric `member_id`s still resolve — they are kept in a permanent mapping table. For most guilds
this makes cutover a non-event for everyone except the officers.

The shim is deprecated from the day it ships and will be supported for at least 24 months. Port your
bots to the real API when you get around to it, not on cutover night.

---

## Next

- **[What does not migrate](what-does-not-migrate.md)** — read this before you announce the move.
- **[Reading your verification report](reading-your-verification-report.md)** — one section at a time.
- **[Parallel run and cutover](parallel-run-and-cutover.md)** — run both systems for a few weeks
  first. Recommended.

---

## Start over, and the three sentences again

If you want to throw it all away and start clean: delete the volume, run the wizard again. The
import is idempotent — importing the same source twice creates nothing new — and **your EQdkp
database is untouched**, so there is no way to run out of attempts.

1. **The importer only ever reads.** It never writes to your EQdkp database.
2. **A dry run is the default.** Nothing is written until you press Commit.
3. **You can undo a commit.** A snapshot is taken automatically before anything is written.
