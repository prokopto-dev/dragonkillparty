# Parallel run and cutover

**Audience:** officer. **Recommended for:** any guild with more than a year of history.

A parallel run means EQdkp stays in charge while Dragon Kill Party runs alongside it, kept up to date
by repeated imports, so your officers use the new thing for real before it owns anything. Then you
flip a switch.

**How long:** 2 to 4 weeks, or **one full loot cycle** — whichever is longer. The right rule is "long
enough that every officer has run a raid night in the new system and every member has looked at their
own balance at least once".

**When to skip it:** a young guild, a small pool, or a guild that has already stopped raiding on the
old site. Import, verify, cut over the same day. The parallel run buys confidence, and confidence you
already have is not worth four weeks.

---

## The rule that makes this safe

**Imported pools start in mirror mode.** While a pool is mirroring, the *only* thing allowed to write
points to it is the importer. If an officer or a bot tries to award loot or run a tick in that pool,
they get a clear refusal naming the cutover step:

```
409  pool_is_mirroring
This pool mirrors your EQdkp install and cannot be written to directly.
Cut over (Settings → Import → Cut over) to make it live.
```

This is not friction for its own sake. Two systems both accepting writes is how a parallel run turns
into two divergent histories and a very bad Tuesday. **During the parallel run, EQdkp is the system
of record.** Everything else about the new site — standings, statements, attendance, calendar, loot
history, the API — works normally and is read-only where it matters.

---

## The weekly rhythm

| Where | What officers do |
|---|---|
| **EQdkp** | Everything real. Add raids, award loot, make adjustments. Unchanged |
| **Dragon Kill Party** | Look at it. Sign up for raids on the calendar. Check standings and statements. Get used to the loot flow. Point one bot at it and watch |
| **Once a week** | Run a **delta import**. One click if you set up a live database connection; upload a fresh backup zip otherwise |

Delta imports get faster and quieter every week. By the third one the balance-delta table should be
nearly empty, because the residual has already been posted and only the new raids move.

### What a delta import does

1. **Confirms it is the same database.** If the fingerprint does not match it **refuses loudly** —
   importing a different database would splice two histories together.
2. **New rows** are inserted exactly as a first import would.
3. **Changed rows:** names, ranks and notes are updated in place. A changed **point** value — a raid
   value edited, an item price corrected, an attendee added — is never edited. It is **reversed and
   re-posted**, so the old number stays visible in the member's statement and the correction is
   dated. That is the same rule the ledger applies to everything.
4. **Deleted rows** produce a reversal. Nothing is ever hard-deleted.
5. **The residual is re-computed**, reversed, and re-posted, so there is exactly one live legacy
   reconciliation per member per pool and its history shows how it moved.
6. **A delta report** comes out in the same format, showing only what changed.

Importing the same thing twice creates nothing new. You cannot double-count by re-running.

### The one case a delta cannot handle

If somebody empties and re-fills a table in EQdkp, primary keys get reused, and an old ID now means a
different thing. The importer detects this — an old ID whose contents changed and whose date went
*backwards* — and quarantines it rather than guessing. If you see that, stop and ask before
continuing.

---

## Two weeks out

- **Announce the date.** Pick a day with no raid on it.
- **Post everyone's statement link.** A member who has already checked their own ten years of loot
  does not argue on cutover night.
- **Have every officer run one raid night in the new UI**, even though the pool is mirroring — the
  calendar, signups, the loot flow and the reconciliation queue all work.
- **Decide about your forum.** If your forum currently logs in against EQdkp, it stops working when
  EQdkp goes away. See [what does not migrate](what-does-not-migrate.md#eqdkp-as-a-login-provider-for-something-else).
- **Get claim rates up.** Watch the claim-progress counter. Chase the stragglers now, not on the
  night.

## One week out

- **Choose your forward point rules.** Imported history is frozen as history and stays under the
  import strategy forever; what you pick here governs everything from cutover onward. If you used
  decay or caps, the wizard pre-fills the rates it detected — confirm them rather than retyping.
- **Point one bot at the compat shim in anger**, on the mirroring instance, and confirm it reads what
  it expects.
- **Run a delta import and read §14** of the report end to end.

---

## The cutover checklist

The same list appears in the UI with a checkbox per line. Do them in this order.

1. **Announce.** Post the cutover time and every member's statement link one more time.
2. **Freeze EQdkp writes.** Put it in maintenance mode, or revoke raid-add, item-add and
   adjustment-add from every group. **Do this before step 3, not after** — a raid added between the
   final import and the flip is a raid you will re-enter by hand.
3. **Run the final delta import.**
4. **Verify.** The report header is green, §3's balance-delta table is empty, and §14 integrity all
   passes. If any of the three is not true, stop. You have lost nothing: EQdkp is frozen, not gone.
5. **Flip the pools from mirror to live.** One click, audited, and it revokes the importer's write
   access in the same action.
6. **Point your bots at the compat shim.** Change one base URL:
   `https://<your-host>/api/compat/eqdkp/api.php`. Your old numeric member IDs still resolve, so most
   bots need nothing else. **This is the step that makes cutover a non-event for everyone except
   you.**
7. **Set EQdkp read-only and leave it up for 90 days.** It costs nothing and it settles arguments.
8. **Archive the final backup into Dragon Kill Party's own artifact store.** Your legacy database
   then rides along in the backup you already take, instead of being one more thing to remember.
9. **Rotate and revoke the legacy credentials** you gave the importer, if you used a live connection.

Then announce that it is done, and where to find the verification report.

---

## After cutover

**Your first raid night.** Have two officers on it, run the tick in the new UI, and check one
member's statement immediately afterwards. This is the moment the guild decides whether the migration
worked.

**Keep the old site up, read-only, for 90 days.** Then archive it — you still have the final backup
inside Dragon Kill Party, so archiving is deleting the web hosting, not deleting the data.

**Port your bots when you feel like it.** The compat shim is deprecated from day one but is supported
for at least 24 months, and it tells you in its response headers which of your tokens is still using
it, so you can see exactly which bot to migrate.

---

## If cutover goes wrong

| Problem | What to do |
|---|---|
| A member's number is wrong on the night | Open their statement. Follow [the decision procedure](reading-your-verification-report.md#a-member-says-their-number-is-wrong). Do not undo anything for one member — a wrong number is fixable with a visible adjustment, and undoing is not |
| Something is broadly wrong and you want out | EQdkp is frozen, not deleted. Un-freeze it, keep raiding there, and take the new site back to mirror mode. Nothing on the old side changed at any point |
| You want to undo the import entirely | **Undo this import** restores the pre-import snapshot exactly. The button tells you how many changes have happened since, so you can see what undoing costs |
| You have already run raids in the new system and still want to unwind | Use the logical revert instead of the snapshot. It reverses every imported point batch, refuses to touch anything that has been referenced since, and lists what it refused and why |
| A raid was entered in EQdkp after the final import | Enter it once in the new system by hand. Do not run another delta after cutting over — the pools are live and the importer no longer has a write path |

The whole shape of this is deliberate: at every step you have either a frozen original, a snapshot,
or a reversal. There is no point in this process where the only copy of your guild's history is
somewhere you cannot get back to.
