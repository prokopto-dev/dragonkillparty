package strategy

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// The earn family's config schemas and the structs they are parsed into, checked against each other.
//
// INTERNAL (`package strategy`), like fixed_price_config_test.go and for the same reason: the thing
// under test is the agreement between an unexported struct's json tags and an unexported schema
// constant. Testing it from outside would mean exporting one of them to make the test possible.
//
// WHY IT EARNS ITS KEEP. The schema renders the pool-settings form and validates what an officer
// types; the struct is what the planner reads. Nothing in Go makes them agree. A knob added to the
// schema and not to the struct is a control the settings form offers and nothing acts on — the worst
// kind of bug in a DKP system, because the officer sets it, believes it, and argues from it. A knob
// in the struct and not the schema is unreachable through the API and gets set only by an importer or
// a test, which is how two pools end up running different rules.
//
// It is a TABLE over the three strategies rather than three files, because the assertion is identical
// and the value is in covering all of them. schemaProperty and the shape of these tests come from
// fixed_price_config_test.go; that file stays as it is, so its strategy keeps its own worked
// commentary.

// earnConfigCases pairs each strategy's schema with the defaults its parser applies.
func earnConfigCases() []struct {
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
		{tickID, tickConfigSchema, defaultTickConfig(), reflect.TypeOf(tickConfig{})},
		{capID, capConfigSchema, defaultCapConfig(), reflect.TypeOf(capConfig{})},
		{
			startPointsID, startPointsConfigSchema, defaultStartPointsConfig(),
			reflect.TypeOf(startPointsConfig{}),
		},
	}
}

// parseEarnSchema decodes a shipped schema into its properties, asserting the document's own shape
// first.
func parseEarnSchema(t *testing.T, schema string) map[string]schemaProperty {
	t.Helper()

	var doc struct {
		Type                 string                    `json:"type"`
		AdditionalProperties *bool                     `json:"additionalProperties"`
		Properties           map[string]schemaProperty `json:"properties"`
	}

	require.NoError(t, json.Unmarshal([]byte(schema), &doc),
		"the shipped config schema must be valid JSON — it is served to the SPA and to every SDK")

	require.Equal(t, "object", doc.Type)
	require.NotNil(t, doc.AdditionalProperties)
	require.False(t, *doc.AdditionalProperties,
		"additionalProperties must be false: a typo'd knob has to be a validation error at the edge, "+
			"not a silently ignored key that leaves the pool running the default")
	require.NotEmpty(t, doc.Properties)

	return doc.Properties
}

// TestEarnStrategies_ConfigSchema_DeclareExactlyTheParsedKnobs closes the naming half of the gap.
func TestEarnStrategies_ConfigSchema_DeclareExactlyTheParsedKnobs(t *testing.T) {
	t.Parallel()

	for _, tc := range earnConfigCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			schema := parseEarnSchema(t, tc.schema)

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

// TestEarnStrategies_ConfigSchema_DefaultsMatchTheParserDefaults closes the value half.
//
// The struct's defaults are what a pool that has set nothing actually runs under — the config JSON is
// decoded OVER them, so an absent key means the Go default and not the schema's. If the two disagree,
// the settings form shows one number and the ledger uses another, and the guild finds out from a
// balance.
//
// `cap`'s defaults are deliberately NOT a runnable configuration (both ceilings unset, which
// validateCapConfig refuses), and that does not weaken this assertion: the form must still show the
// same unset values the parser starts from.
func TestEarnStrategies_ConfigSchema_DefaultsMatchTheParserDefaults(t *testing.T) {
	t.Parallel()

	for _, tc := range earnConfigCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			schema := parseEarnSchema(t, tc.schema)

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

// TestEarnStrategies_ConfigSchema_MoneyKnobsAreIntegers restates canonical §1 where a schema could
// break it: `number` in a JSON Schema permits 12.5, and a decimal in the point path is a float.
//
// An `array` is allowed through because its ITEMS carry the types — role_multipliers is a list of
// objects, and the recursion that checks those is
// TestProperty_NoFloat_EarnConfigSchemas_DeclareNoNumber, which walks every nested property of every
// shipped schema.
func TestEarnStrategies_ConfigSchema_MoneyKnobsAreIntegers(t *testing.T) {
	t.Parallel()

	for _, tc := range earnConfigCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			for name, prop := range parseEarnSchema(t, tc.schema) {
				switch prop.Type {
				case "string", "array", "object":
					continue
				default:
					require.Equal(t, "integer", prop.Type,
						"knob %q is declared as %q. Money is integer centipoints (_cp) and ratios "+
							"are integer basis points (_bp); there is no decimal anywhere in the "+
							"point path.", name, prop.Type)
				}
			}
		})
	}
}

// TestEarnStrategies_DefaultConfig_ValidatesOrRefusesDeliberately pins what a pool that has set
// nothing does.
//
// Two of the three run their defaults; `cap` refuses its own, because there is no defensible default
// ceiling and inventing one would silently trim a guild's balances. That asymmetry is a decision
// rather than an oversight, and a test is where a decision like that stops being invisible.
func TestEarnStrategies_DefaultConfig_ValidatesOrRefusesDeliberately(t *testing.T) {
	t.Parallel()

	if _, err := validateTickConfig(defaultTickConfig()); err != nil {
		t.Fatalf("tick's shipped defaults do not validate: %v", err)
	}

	if _, err := validateStartPointsConfig(defaultStartPointsConfig()); err != nil {
		t.Fatalf("start_points' shipped defaults do not validate: %v", err)
	}

	_, err := validateCapConfig(defaultCapConfig())
	require.ErrorIs(t, err, ErrInvalidConfig,
		"cap must refuse a pool that names no ceiling: running the cap strategy while capping "+
			"nothing is a settings page that says Cap and a standings page with an uncapped veteran")
}
