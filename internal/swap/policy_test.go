package swap_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/swap"
)

// Policy validation. The rule these tests are written against: a policy that cannot be evaluated is
// REFUSED, never defaulted. A settings form that accepted a broken rule and quietly ran the shipped
// one would price swaps under rules nobody chose, and the guild would find out from a member's
// statement months later.

// TestPolicy_Default_MatchesTheDesignAndValidates pins the shipped policy against
// docs/design/10-ui-decisions.md §4's table.
//
// A WHOLE-VALUE comparison, because the interesting failure is a rule that silently changed shape —
// a magnitude edited, a rule flipped on, the order shuffled — and asserting three fields would hide
// exactly that. The literal below is the design's table transcribed; if this test needs editing, the
// design changed and the diff should say so.
func TestPolicy_Default_MatchesTheDesignAndValidates(t *testing.T) {
	t.Parallel()

	want := swap.Policy{
		BaseCostCp: 75_000,
		Rules: []swap.Rule{
			{Kind: swap.RuleOpenWindow, Enabled: true},
			{Kind: swap.RuleAnnualAllowance, Enabled: true, PerYear: 2},
			{Kind: swap.RuleTargetLevel, Enabled: true, AmountCp: 25_000, MinLevel: 60},
			{Kind: swap.RuleNeededClass, Enabled: true, AmountCp: 30_000},
			{
				Kind: swap.RuleAttendance, Enabled: false, AmountCp: 10_000,
				MinAttendanceBp: 8_000, WindowDays: 90,
			},
			{Kind: swap.RuleReturningMain, Enabled: false, FactorBp: 20_000},
		},
		Guards: swap.Guards{
			CooldownDays:         90,
			MinAttendanceBp:      5_000,
			AttendanceWindowDays: 30,
			ApprovalsRequired:    2,
		},
	}

	require.Equal(t, want, swap.DefaultPolicy())
	require.NoError(t, swap.DefaultPolicy().Validate())

	// The needed-class list ships EMPTY on purpose: the mockup's Cleric and Enchanter are one guild's
	// roster gap, not a default anybody else should inherit.
	require.Empty(t, swap.DefaultPolicy().Rules[3].Classes)
}

// TestPolicy_Default_RoundTripsThroughJSON: Phase 7 snapshots the policy beside the quote it
// produced, so a document that does not survive the round trip is a held quote whose rules cannot be
// read back.
func TestPolicy_Default_RoundTripsThroughJSON(t *testing.T) {
	t.Parallel()

	body, err := json.Marshal(swap.DefaultPolicy())
	require.NoError(t, err)

	// Money is an unquoted integer on the wire (canonical §1). The type carries its own marshaller;
	// this asserts the policy document did not wrap it in anything.
	require.Contains(t, string(body), `"base_cost_cp":75000`)

	var back swap.Policy

	require.NoError(t, json.Unmarshal(body, &back))
	require.Equal(t, swap.DefaultPolicy(), back)
	require.NoError(t, back.Validate())
}

// TestPolicy_Validate_RejectsTheDocumentAndSaysWhy is the validation table.
//
// Each case mutates the shipped default in exactly one way, so what the case is about is the diff
// from a policy that is known to validate. The `want` string is a fragment of the message an officer
// would read: a rejection that does not name the field is a rejection they cannot act on.
func TestPolicy_Validate_RejectsTheDocumentAndSaysWhy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(p *swap.Policy)
		want   string
	}{
		{
			name:   "a negative base cost would pay a member to swap",
			mutate: func(p *swap.Policy) { p.BaseCostCp = -1 },
			want:   "base_cost_cp is -1",
		},
		{
			name: "a kind outside the catalogue is refused rather than skipped",
			mutate: func(p *swap.Policy) {
				p.Rules = append(p.Rules, swap.Rule{Kind: "guild_leader_says_so", Enabled: true})
			},
			want: `kind "guild_leader_says_so", which is not one of the six main-swap rules`,
		},
		{
			name: "the same kind twice would apply its effect twice",
			mutate: func(p *swap.Policy) {
				p.Rules = append(p.Rules, swap.Rule{
					Kind: swap.RuleTargetLevel, Enabled: true, AmountCp: 100, MinLevel: 50,
				})
			},
			want: `repeats kind "target_level"`,
		},
		{
			name:   "a parameter the kind does not read is a discount somebody thinks they restricted",
			mutate: func(p *swap.Policy) { p.Rules[3].MinLevel = 60 },
			want:   `sets "min_level", which that kind does not read`,
		},
		{
			name:   "an allowance of zero can never match",
			mutate: func(p *swap.Policy) { p.Rules[1].PerYear = 0 },
			want:   "grants 0 free swaps per period",
		},
		{
			name:   "a level above the cap can never match",
			mutate: func(p *swap.Policy) { p.Rules[2].MinLevel = 61 },
			want:   "has min_level 61, want 1..60",
		},
		{
			name:   "a level below one matches everybody, which is not a level rule",
			mutate: func(p *swap.Policy) { p.Rules[2].MinLevel = -1 },
			want:   "has min_level -1, want 1..60",
		},
		{
			name:   "a discount of nothing is a rule nobody finished configuring",
			mutate: func(p *swap.Policy) { p.Rules[2].AmountCp = 0 },
			want:   "discounts 0 centipoints",
		},
		{
			name:   "a negative discount would be a surcharge wearing a discount's name",
			mutate: func(p *swap.Policy) { p.Rules[2].AmountCp = -100 },
			want:   "discounts -100 centipoints",
		},
		{
			name:   "a factor of zero would zero every price",
			mutate: func(p *swap.Policy) { p.Rules[5].FactorBp = 0 },
			want:   "has factor_bp 0",
		},
		{
			name:   "a negative factor is not a multiplier",
			mutate: func(p *swap.Policy) { p.Rules[5].FactorBp = -20_000 },
			want:   "has factor_bp -20000",
		},
		{
			name:   "an attendance floor above 100% can never match",
			mutate: func(p *swap.Policy) { p.Rules[4].MinAttendanceBp = 10_001 },
			want:   "has min_attendance_bp 10001, want 1..10000",
		},
		{
			name:   "an attendance floor of zero matches everybody",
			mutate: func(p *swap.Policy) { p.Rules[4].MinAttendanceBp = 0 },
			want:   "has min_attendance_bp 0, want 1..10000",
		},
		{
			name:   "an attendance window of zero days measures nothing",
			mutate: func(p *swap.Policy) { p.Rules[4].WindowDays = 0 },
			want:   "measures attendance over 0 days, want 1..3650",
		},
		{
			name:   "an attendance window past ten years is a typo",
			mutate: func(p *swap.Policy) { p.Rules[4].WindowDays = 3_651 },
			want:   "measures attendance over 3651 days, want 1..3650",
		},
		{
			name:   "a class spelled for a human misses the comparison",
			mutate: func(p *swap.Policy) { p.Rules[3].Classes = []string{"Cleric"} },
			want:   `lists class "Cleric" at position 0`,
		},
		{
			name:   "an empty class is not a class",
			mutate: func(p *swap.Policy) { p.Rules[3].Classes = []string{"cleric", ""} },
			want:   `lists class "" at position 1`,
		},
		{
			name:   "a trailing underscore is not snake_case",
			mutate: func(p *swap.Policy) { p.Rules[3].Classes = []string{"cleric_"} },
			want:   `lists class "cleric_"`,
		},
		{
			name:   "a doubled underscore is not snake_case",
			mutate: func(p *swap.Policy) { p.Rules[3].Classes = []string{"shadow__knight"} },
			want:   `lists class "shadow__knight"`,
		},
		{
			name:   "a leading underscore is not snake_case",
			mutate: func(p *swap.Policy) { p.Rules[3].Classes = []string{"_cleric"} },
			want:   `lists class "_cleric"`,
		},
		{
			name:   "a hyphen is not snake_case, however much it looks like one",
			mutate: func(p *swap.Policy) { p.Rules[3].Classes = []string{"shadow-knight"} },
			want:   `lists class "shadow-knight"`,
		},
		{
			name:   "a class starting with a digit is not snake_case",
			mutate: func(p *swap.Policy) { p.Rules[3].Classes = []string{"1cleric"} },
			want:   `lists class "1cleric"`,
		},
		{
			name:   "the same class twice",
			mutate: func(p *swap.Policy) { p.Rules[3].Classes = []string{"cleric", "cleric"} },
			want:   `lists class "cleric" twice`,
		},
		{
			name: "an unnamed window cannot be reported as the reason a swap was free",
			mutate: func(p *swap.Policy) {
				p.Windows = []swap.Window{{StartsAt: 1_000, EndsAt: 2_000}}
			},
			want: "window 0 has no name",
		},
		{
			name: "a window starting at the epoch is not an instant",
			mutate: func(p *swap.Policy) {
				p.Windows = []swap.Window{{Name: "Autumn window", StartsAt: 0, EndsAt: 2_000}}
			},
			want: "starts at 0, which is not an instant",
		},
		{
			name: "a window that ends where it starts covers no instant",
			mutate: func(p *swap.Policy) {
				p.Windows = []swap.Window{{Name: "Autumn window", StartsAt: 2_000, EndsAt: 2_000}}
			},
			want: "ends at 2000, at or before its start 2000",
		},
		{
			name:   "a negative cooldown",
			mutate: func(p *swap.Policy) { p.Guards.CooldownDays = -1 },
			want:   "guards.cooldown_days is -1, want 0..3650",
		},
		{
			name:   "a cooldown past ten years",
			mutate: func(p *swap.Policy) { p.Guards.CooldownDays = 3_651 },
			want:   "guards.cooldown_days is 3651, want 0..3650",
		},
		{
			name:   "an attendance floor above 100%",
			mutate: func(p *swap.Policy) { p.Guards.MinAttendanceBp = 10_001 },
			want:   "guards.min_attendance_bp is 10001, want 0..10000",
		},
		{
			name:   "a negative attendance window",
			mutate: func(p *swap.Policy) { p.Guards.AttendanceWindowDays = -1 },
			want:   "guards.attendance_window_days is -1, want 0..3650",
		},
		{
			name:   "a floor with no window has nothing to measure over",
			mutate: func(p *swap.Policy) { p.Guards.AttendanceWindowDays = 0 },
			want:   "a floor needs a window to be measured over",
		},
		{
			name:   "a negative approval count",
			mutate: func(p *swap.Policy) { p.Guards.ApprovalsRequired = -1 },
			want:   "guards.approvals_required is -1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := swap.DefaultPolicy()
			tc.mutate(&p)

			err := p.Validate()
			require.ErrorIs(t, err, swap.ErrInvalidPolicy)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestPolicy_Validate_ChecksDisabledRulesToo. A parameter that only breaks on the day an officer
// flips the toggle breaks during a raid night, which is the worst moment this repository has.
func TestPolicy_Validate_ChecksDisabledRulesToo(t *testing.T) {
	t.Parallel()

	p := swap.DefaultPolicy()
	require.False(t, p.Rules[5].Enabled, "the returning-main rule ships disabled")

	p.Rules[5].FactorBp = 0

	err := p.Validate()
	require.ErrorIs(t, err, swap.ErrInvalidPolicy)
	require.Contains(t, err.Error(), "has factor_bp 0")
}

// TestPolicy_Validate_AcceptsTheLegalEdges is the other half of the table above: the shapes that look
// like mistakes and are not. Without it, tightening a bound until the package rejects a policy a
// guild legitimately wants would pass every test.
func TestPolicy_Validate_AcceptsTheLegalEdges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(p *swap.Policy)
	}{
		{
			name:   "a free base cost — a guild that prices swaps at nothing",
			mutate: func(p *swap.Policy) { p.BaseCostCp = 0 },
		},
		{
			name:   "an empty needed-class list — a guild that has named no needed class",
			mutate: func(p *swap.Policy) { p.Rules[3].Classes = nil },
		},
		{
			name:   "no rules at all — every swap costs the base",
			mutate: func(p *swap.Policy) { p.Rules = nil },
		},
		{
			name:   "no cooldown",
			mutate: func(p *swap.Policy) { p.Guards.CooldownDays = 0 },
		},
		{
			name: "no attendance floor, window kept",
			mutate: func(p *swap.Policy) {
				p.Guards.MinAttendanceBp = 0
			},
		},
		{
			name: "no attendance floor and no window",
			mutate: func(p *swap.Policy) {
				p.Guards.MinAttendanceBp = 0
				p.Guards.AttendanceWindowDays = 0
			},
		},
		{
			name:   "no approvals — a guild that auto-approves",
			mutate: func(p *swap.Policy) { p.Guards.ApprovalsRequired = 0 },
		},
		{
			name:   "a factor below 1.0, which is a discount expressed as a scale",
			mutate: func(p *swap.Policy) { p.Rules[5].FactorBp = 5_000 },
		},
		{
			name: "a class with digits and interior underscores",
			mutate: func(p *swap.Policy) {
				p.Rules[3].Classes = []string{"shadow_knight", "class_2"}
			},
		},
		{
			name: "rules in a different order — the order is the guild's, not this package's",
			mutate: func(p *swap.Policy) {
				p.Rules = []swap.Rule{p.Rules[5], p.Rules[2], p.Rules[0]}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := swap.DefaultPolicy()
			tc.mutate(&p)

			require.NoError(t, p.Validate())
		})
	}
}

// TestPolicy_Validate_NamesTheFirstFailureOnly. The caller is a settings form that highlights one
// field; a joined list of every failure reads worse than the one an officer must fix to see the next.
func TestPolicy_Validate_NamesTheFirstFailureOnly(t *testing.T) {
	t.Parallel()

	p := swap.DefaultPolicy()
	p.Rules[2].MinLevel = 61
	p.Rules[4].WindowDays = 0

	err := p.Validate()
	require.ErrorIs(t, err, swap.ErrInvalidPolicy)
	require.Contains(t, err.Error(), "min_level 61")
	require.NotContains(t, err.Error(), "0 days")
	require.Equal(t, 1, strings.Count(err.Error(), "rule "), "one failure, one sentence")
}
