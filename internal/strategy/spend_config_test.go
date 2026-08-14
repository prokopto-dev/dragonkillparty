package strategy

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// The spend family's config schemas and the structs they are parsed into, checked against each other.
// Phase 1, #195.
//
// INTERNAL (`package strategy`), like fixed_price_config_test.go and earn_config_test.go and for the
// same reason: the thing under test is the agreement between an unexported struct's json tags and an
// unexported schema constant. Testing it from outside would mean exporting one of them to make the
// test possible.
//
// WHY IT EARNS ITS KEEP. The schema renders the pool-settings form and validates what an officer
// types; the struct is what the planner reads. Nothing in Go makes them agree. A knob added to the
// schema and not to the struct is a control the settings form offers and nothing acts on — the worst
// kind of bug in a DKP system, because the officer sets it, believes it, and argues from it. A knob
// in the struct and not the schema is unreachable through the API and gets set only by an importer or
// a test, which is how two pools end up running different rules.

// spendConfigCases pairs each strategy's schema with the defaults its parser applies.
func spendConfigCases() []struct {
	id       string
	schema   string
	defaults any
	parsed   reflect.Type
} {
	return []struct {
		id       string
		schema   string
		defaults any
		parsed   reflect.Type
	}{
		{
			auctionOpenID, auctionOpenConfigSchema, defaultAuctionOpenConfig(),
			reflect.TypeOf(auctionOpenConfig{}),
		},
		{
			auctionSealedID, auctionSealedConfigSchema, defaultAuctionSealedConfig(),
			reflect.TypeOf(auctionSealedConfig{}),
		},
		{
			relativeBidID, relativeBidConfigSchema, defaultRelativeBidConfig(),
			reflect.TypeOf(relativeBidConfig{}),
		},
		{rollID, rollConfigSchema, defaultRollConfig(), reflect.TypeOf(rollConfig{})},
	}
}

// TestSpendStrategies_ConfigSchema_DeclareExactlyTheParsedKnobs closes the naming half of the gap.
func TestSpendStrategies_ConfigSchema_DeclareExactlyTheParsedKnobs(t *testing.T) {
	t.Parallel()

	for _, tc := range spendConfigCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			schema := parseShippedSchema(t, tc.schema)

			declared := make([]string, 0, len(schema))
			for name := range schema {
				declared = append(declared, name)
			}

			parsed := make([]string, 0, len(declared))

			for i := range tc.parsed.NumField() {
				tag, ok := tc.parsed.Field(i).Tag.Lookup("json")
				require.True(t, ok, "field %s has no json tag, so no schema property can name it",
					tc.parsed.Field(i).Name)

				parsed = append(parsed, tag)
			}

			sort.Strings(declared)
			sort.Strings(parsed)

			require.Equal(t, parsed, declared,
				"the config schema and %s's parsed struct must name the same knobs. A knob only in "+
					"the schema is a control the settings form offers and the planner ignores; a knob "+
					"only in the struct is one the API cannot reach.", tc.id)
		})
	}
}

// TestSpendStrategies_ConfigSchema_DefaultsMatchTheParserDefaults closes the value half.
//
// The struct's defaults are what a pool that has set nothing actually runs under — the config JSON is
// decoded OVER them, so an absent key means the Go default and not the schema's. If the two disagree,
// the settings form shows one number and the ledger uses another, and the guild finds out from a
// balance.
func TestSpendStrategies_ConfigSchema_DefaultsMatchTheParserDefaults(t *testing.T) {
	t.Parallel()

	for _, tc := range spendConfigCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			schema := parseShippedSchema(t, tc.schema)

			encoded, err := json.Marshal(tc.defaults)
			require.NoError(t, err)

			var applied map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(encoded, &applied))

			for name, prop := range schema {
				require.NotNil(t, prop.Default,
					"knob %q declares no default, so the settings form has nothing to show for a "+
						"pool that has never set it", name)
				require.JSONEq(t, string(prop.Default), string(applied[name]),
					"knob %q defaults to %s in the schema and to %s in the parser",
					name, prop.Default, applied[name])
			}
		})
	}
}

// TestSpendStrategies_ConfigSchema_MoneyKnobsAreIntegers restates canonical §1 where a schema could
// break it: `number` in a JSON Schema permits 12.5, and a decimal in the point path is a float.
func TestSpendStrategies_ConfigSchema_MoneyKnobsAreIntegers(t *testing.T) {
	t.Parallel()

	for _, tc := range spendConfigCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			for name, prop := range parseShippedSchema(t, tc.schema) {
				if prop.Type == "string" {
					continue
				}

				require.Equal(t, "integer", prop.Type,
					"knob %q is declared as %q. Money is integer centipoints (_cp) and ratios are "+
						"integer basis points (_bp); there is no decimal anywhere in the point path.",
					name, prop.Type)
			}
		})
	}
}

// TestSpendStrategies_DefaultConfig_IsRunnable pins what a pool that has set nothing does.
//
// All four run their own defaults, and that is a decision rather than an accident: `cap` refuses its
// defaults because there is no defensible default ceiling, whereas every rule here has one — a
// one-point minimum, second price, any share up to the whole bank, and `/random 1 100` for free. A
// spend rule that refused its own defaults would be a pool that could save its settings and then not
// award an item.
func TestSpendStrategies_DefaultConfig_IsRunnable(t *testing.T) {
	t.Parallel()

	if _, err := validateAuctionOpenConfig(defaultAuctionOpenConfig()); err != nil {
		t.Fatalf("auction_open's shipped defaults do not validate: %v", err)
	}

	if _, err := validateAuctionSealedConfig(defaultAuctionSealedConfig()); err != nil {
		t.Fatalf("auction_sealed's shipped defaults do not validate: %v", err)
	}

	if _, err := validateRelativeBidConfig(defaultRelativeBidConfig()); err != nil {
		t.Fatalf("relative_bid's shipped defaults do not validate: %v", err)
	}

	if _, err := validateRollConfig(defaultRollConfig()); err != nil {
		t.Fatalf("roll's shipped defaults do not validate: %v", err)
	}
}

// TestSpendStrategies_Terms_ReachEveryValidator is the shared half of every config in this family.
//
// `proceeds` and `solo_policy` mean the same thing in five strategies and are validated in one place
// (validateSpendTerms), which is only true while every strategy's validator actually calls it. A
// strategy that stopped would accept a destination the others refuse, and the award would land
// somewhere nobody chose.
func TestSpendStrategies_Terms_ReachEveryValidator(t *testing.T) {
	t.Parallel()

	bad := spendTerms{Proceeds: "the_officer", SoloPolicy: SoloPolicyGuildBank}

	for _, tc := range []struct {
		id      string
		refused func() error
	}{
		{auctionOpenID, func() error {
			cfg := defaultAuctionOpenConfig()
			cfg.Proceeds = bad.Proceeds
			_, err := validateAuctionOpenConfig(cfg)

			return err
		}},
		{auctionSealedID, func() error {
			cfg := defaultAuctionSealedConfig()
			cfg.Proceeds = bad.Proceeds
			_, err := validateAuctionSealedConfig(cfg)

			return err
		}},
		{relativeBidID, func() error {
			cfg := defaultRelativeBidConfig()
			cfg.Proceeds = bad.Proceeds
			_, err := validateRelativeBidConfig(cfg)

			return err
		}},
		{rollID, func() error {
			cfg := defaultRollConfig()
			cfg.Proceeds = bad.Proceeds
			_, err := validateRollConfig(cfg)

			return err
		}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			err := tc.refused()
			require.ErrorIs(t, err, ErrInvalidConfig)
			require.ErrorContains(t, err, tc.id, "the refusal names the strategy that refused")
		})
	}
}
