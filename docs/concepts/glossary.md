# Glossary

Two vocabularies collide in this product: EverQuest raiding and double-entry accounting. A reader
fluent in one is usually lost in the other. Entries are one sentence and link to the page that
explains them properly.

## Product and ledger

| Term | Means |
|---|---|
| **Person** | One human being in the guild; the thing that owns a balance. |
| **Character** | One in-game name on one server, belonging to exactly one person; attribution only, never the owner of points. |
| **Account** | The ledger-side identity of a person within a pool — what entries are posted against. |
| **User** | A login (local password, Discord, or OIDC), which controls access rather than points. |
| **Claim** | The act of a member proving a character is theirs, by officer approval, a roster dump, or a one-time phrase spoken in game. |
| **Pool** | One named currency with its own strategy, configuration, alt policy and attendance windows. |
| **Balance kind** | Which quantity an entry moves — `dkp`, or `ep`/`gp` under EPGP, or `sk_position` under Suicide Kings. |
| **Centipoint** | The storage unit for points: points × 100, as a 64-bit integer, so nothing ever rounds badly. |
| **Ledger batch** | One economic event, committed whole; carries the strategy, its version, a configuration snapshot, the actor and the reason. |
| **Ledger entry** | One account's share of a batch. |
| **`seq`** | The per-pool monotonic counter; a balance is defined *as of a `seq`*, never as of a timestamp. |
| **`event_seq`** | The global outbox counter used for realtime delivery and replay — a different number from `seq`, deliberately never given the same name. |
| **`effective_at`** | Game truth: when the thing happened in the guild's world. May be backdated. |
| **`recorded_at`** | System truth: when the software learned about it. Never backdated. |
| **Reversal** | A new batch that negates an earlier one and points at it; the only way to correct a mistake. |
| **Correction** | A compensating batch posted at today's `seq` after a retroactive edit, rather than a rewrite of history. |
| **Snapshot** | The derived cache of current balances, rebuilt nightly from the log and verified against it. |
| **Invariant** | A rule the ledger enforces on every batch regardless of what a strategy proposes. |
| **Strategy** | A pure function that proposes ledger batches; it never touches the database. |
| **Planner** | One of a strategy's methods — `plan_attendance`, `plan_award`, `plan_decay`, `plan_reversal`. |
| **Proposal** | What a planner returns: entries plus metadata, not yet a write. |
| **Idempotency key** | A client-supplied key making a retried `POST` return the original result instead of creating a second one. |
| **Artifact** | An uploaded raw file — a `RaidRoster` dump, a `/who` paste, a log slice — retained as dispute evidence. |
| **Reconciliation queue** | Where a name or item a parser could not resolve waits for an officer, instead of being dropped. |
| **Dispute** | A member's formal objection to a specific tick or entry, resolved by an officer and linked to any correction. |
| **Service account** | A non-human identity that owns API tokens, so a bot survives its officer leaving the guild. |
| **PAT** | Personal access token — the opaque `dkp_pat_…` bearer credential; there is no all-powerful one. |
| **Scope** | A `family:verb` restriction on a token; effective capability is role permissions ∩ token scopes. |
| **Step-up** | A required re-authentication within the last few minutes, needed for token minting, role edits, backup download, bulk PII reads and import commit. |
| **Outbox** | The table that realtime events are written into, inside the same transaction as the state change. |
| **Compat shim** | The `api.php` adapter that lets existing EQdkp-era bots keep working; deprecated from the day it ships. |

## DKP systems

| Term | Means |
|---|---|
| **DKP** | Dragon kill points — the earned currency a guild spends on raid loot. The product is *Dragon Kill Party*; the CLI is `dkp`. |
| **Tick** | One attendance snapshot during a raid, worth a fixed number of points to everyone present. |
| **Earned / spent / adjustment** | The three ways a balance moves: attendance and kills, loot, and an officer's manual entry. |
| **Zero-sum** | A closed economy: what a winner pays is redistributed to the other raiders, so the guild total never changes. |
| **Zero-sum residue** | The few centipoints left over when a price does not divide evenly among the recipients; allocated by largest remainder or routed to the guild bank, never rounded away. |
| **Largest-remainder allocation** | The split method that guarantees credits sum to exactly the debit, instead of rounding each credit independently. |
| **Decay (percentage)** | A recurring haircut — 10% a week — posted as explicit ledger batches. |
| **Decay (window)** | Earnings older than N days stop counting, by a hard cutoff or a linear taper. |
| **Cap** | A ceiling on a balance, or a reduced earn rate above a soft ceiling. |
| **Start points** | An opening balance granted once to a new member so recruits can bid on something. |
| **Attendance percentage** | Ticks you attended ÷ qualifying ticks held, over a window. |
| **Attendance window** | The rolling period the percentage is computed over — commonly 30, 60 or 90 days, plus lifetime. |
| **Connected raid** | Several raid rows linked so they count once in raid-based attendance, used when one raid night produced several raid records. |
| **`no_attendance` event** | An event type that still awards points but is excluded from the attendance denominator — off-night pickups, tracking credit. |
| **Effective DKP** | Balance scaled by attendance percentage, used as a *ranking*, never as a spendable balance. |
| **EPGP** | Effort points and gear points, with priority `EP / max(GP, base_GP)`; both decay identically so decay does not reorder standings. |
| **Suicide Kings** | A total ordering of members where the winner drops to the bottom, behind the last attendee, or swaps within the attendees. |
| **Loot council** | Officers vote; there is no arithmetic, only a recorded decision and a rationale. |
| **Rot** | An item nobody bid on. |
| **Off-spec** | An item taken for a secondary role, often at a discount. |
| **Twink / alt** | A non-main character; whether it earns and whether it may bid is guild policy. |
| **Main** | The character a person raids on, and the one their balance is nominally attached to. |
| **Pilot / box** | Playing a second account, or playing someone else's character; attendance records `pilot` distinctly from `present`. |
| **Min bid** | The floor an auction opens at, often tiered by item. |
| **Increment** | The step every subsequent bid must clear. |
| **Anti-snipe** | Extending an auction's close when a bid lands near the deadline, bounded by a maximum extension count. |
| **Sealed bid** | Bids hidden from everyone, including officers, until the session enters `closing`. |
| **Second price** | The winner pays the runner-up's bid plus one increment, which removes the incentive to bid your whole bank. |
| **Relative bid** | A bid expressed as a percentage of the bidder's balance, resolved against the snapshot frozen at session open. |
| **Hold** | Points reserved against an accepted bid, so the same points cannot win two simultaneous auctions. |
| **Tie-break chain** | The ordered, configurable list of rules resolving equal bids, recorded on the resolution so it can be explained in chat. |

## EverQuest and Project 1999

| Term | Means |
|---|---|
| **P99** | Project 1999, a volunteer EverQuest emulator frozen at the Scars of Velious expansion. |
| **Blue / Green / Red** | The three P99 servers: the permanent PvE server, the time-locked progression server, and the PvP server. |
| **Titanium client** | The 2005 retail EverQuest client P99 runs on; it cannot be modded, so there are no addons and no in-game DKP UI. |
| **MacroQuest** | Third-party automation software; **bannable on P99**, which is why every tool here is read-only and out of process. |
| **Era** | Which content is unlocked — Classic, Kunark or Velious. Level cap 60, no alternate advancement, no Bazaar. |
| **ToV** | Temple of Veeshan, the Velious raid zone; **NToV** is its north wing, the top of the P99 loot pyramid, ending at Vulak\`Aerr. |
| **eqlog file** | `eqlog_<Character>_<server>.txt` — the per-character chat and system log, enabled with `/log on`, and the only machine-readable output the client produces. |
| **`/who`** | An in-zone player listing; the *de facto* attendance snapshot, but capped well below a 60-person raid, so officers run it per class. |
| **ANONYMOUS** | A `/who` entry for a player hiding their level and class, which is why some raiders parse without a class. |
| **Class title** | The level-appropriate name `/who` and raid dumps print instead of the class — a level 60 Necromancer shows as "Warlock". |
| **Raid dump / `RaidRoster`** | `RaidRoster-YYYYMMDD-HHMMSS.txt`, produced by clicking Dump on the in-game Raid window: tab-separated group, name, level and class title. |
| **Group 0** | A raid-dump row for someone in the raid but not in a group — the bench. |
| **Guild dump** | A tab-separated roster export; whether the Titanium client supports it is **unverified**, so the importer sniffs the column count and also accepts a pasted roster. |
| **Loot line** | `--Tankguy has looted a Cloak of Flames.--`; the looter is frequently not the winner. |
| **`/random`** | The in-game roll, printed as two consecutive lines that a parser must pair — the roller on the first, the result on the second. |
| **FTE** | First to engage; the server shouts which guild got aggro first, and kill-stealing after an FTE is a petitionable offence. |
| **Race line** | The physical start position defined per raid target, behind which guilds wait for a spawn. |
| **Variance** | The randomised spawn window on raid targets, which is why a raid session can be six hours of waiting punctuated by a kill. |
| **Tracker** | One of the two players per zone permitted to watch for a spawn; they may not pull, heal, buff, or do anything that would appear in an encounter log. |
| **Rotation** | A scheduled sharing of a target between guilds; on P99 most targets are FTE races, but Plane of Sky runs a weekly rotation. |
| **NO DROP** | An item that cannot be traded after looting, which is why the corpse looter and the award winner must be recorded separately. |
| **Slain line** | `Vulak\`Aerr has been slain by Tankguy!`; the automatic kill-credit trigger. |
| **Classes (14)** | Bard, Cleric, Druid, Enchanter, Magician, Monk, Necromancer, Paladin, Ranger, Rogue, Shadow Knight, Shaman, Warrior, Wizard. No Beastlord, no Berserker. |
| **Races (13)** | Barbarian, Dark Elf, Dwarf, Erudite, Gnome, Half-Elf, Halfling, High Elf, Human, Iksar, Ogre, Troll, Wood Elf. Iksar do not exist before Kunark. |

### Class titles by level

A parser that does not carry this table drops most of a raid roster, because raiders are level 51–60
and the dump prints the title, not the class.

| Class | 1–50 | 51–54 | 55–59 | 60 |
|---|---|---|---|---|
| Bard | Bard | Minstrel | Troubadour | Virtuoso |
| Cleric | Cleric | Vicar | Templar | High Priest |
| Druid | Druid | Wanderer | Preserver | Hierophant |
| Enchanter | Enchanter | Illusionist | Beguiler | Phantasmist |
| Magician | Magician | Elementalist | Conjurer | Arch Mage |
| Monk | Monk | Disciple | Master | Grandmaster |
| Necromancer | Necromancer | Heretic | Defiler | Warlock |
| Paladin | Paladin | Cavalier | Knight | Crusader |
| Ranger | Ranger | Pathfinder | Outrider | Warder |
| Rogue | Rogue | Rake | Blackguard | Assassin |
| Shadow Knight | Shadow Knight | Reaver | Revenant | Grave Lord |
| Shaman | Shaman | Mystic | Luminary | Oracle |
| Warrior | Warrior | Champion | Myrmidon | Warlord |
| Wizard | Wizard | Channeler | Evoker | Sorcerer |

Three titles contain a space — Shadow Knight, High Priest, Arch Mage — which breaks naive whitespace
splitting, and four are ordinary English words — Master, Knight, Warder, Champion — which breaks naive
keyword matching. Raid dumps are tab-separated for exactly this reason.

### Eras and their raid content

| Era | Headline raid targets |
|---|---|
| Classic | Lord Nagafen, Lady Vox, Phinigel Autropos, Plane of Fear, Plane of Hate, Plane of Sky |
| Kunark | Trakanon, Gorenaire, Severilous, Talendor, Faydedar, Venril Sathir, Veeshan's Peak, Chardok royals |
| Velious | Kael Drakkel and the Avatar of War, Temple of Veeshan including NToV and Vulak\`Aerr, Sleeper's Tomb, Plane of Growth, the Western Wastes dragons |

Kerafyrm was awakened on Blue in 2016 and on Green in 2022, permanently ending Warder respawns on
those servers — an event catalogue must tolerate an event that exists but can never fire again.

## EQdkp Plus terms, for migrators

| Term | Means here |
|---|---|
| **MultiDKP pool** | A pool. |
| **Item pool** | Which items are purchasable with which currency; kept, cleaned up. |
| **Event** | An event type in the encounter catalogue. |
| **Raid** | One EQdkp raid row becomes one **tick** inside a raid session, not a whole raid. |
| **Connected attendance** | A JSON array of linked raid IDs; becomes a real indexed `attendance_group_id`. |
| **APA** | Automatic point adjustment — EQdkp's decay, cap and start-points jobs; each maps to a strategy here. |
| **Twink** | An alt. EQdkp's twink flag becomes a character under the same person. |
| **Rank hiding** | EQdkp made hiding a property of the *rank*, so guilds invented a fake "Hidden" rank to hide one bank mule. Here it is both: `character.hidden` per character **and** `guild_rank.hidden_default` per rank. Visibility is separate from capability, which is what roles control. |
| **Plus Exchange** | EQdkp's plugin marketplace; there is no equivalent, because the API is the extension point. |
| **`atoken`** | The query-string API token. Accepted **only** by the compat shim, redacted from every log, and counted so you can see which bot still uses it. |
| **PDH** | EQdkp's internal data-handler prefix; it appears in this repository only inside the importer's legacy-name file. |
| **Table prefix** | EQdkp's configurable `__` table prefix, which the importer detects rather than assumes. |

## Next

- [The ledger](ledger.md) · [Invariants](invariants.md) · [Point strategies](strategies.md)
- [Attendance and windows](../guides/attendance-and-windows.md)
- [What does not migrate](../migration/what-does-not-migrate.md)
