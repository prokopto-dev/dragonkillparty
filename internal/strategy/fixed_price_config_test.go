package strategy

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// The config schema and the struct it is parsed into, checked against each other.
//
// This is the one test in the package that is INTERNAL (`package strategy` rather than
// `strategy_test`), because the thing under test is the agreement between an unexported struct's
// json tags and an unexported schema constant. Testing it from outside would mean exporting one of
// them to make the test possible, which is the tail wagging the dog.
//
// WHY IT EARNS ITS KEEP. The schema renders the pool-settings form and validates what an officer
// types; the struct is what the planner reads. Nothing in Go makes them agree. A knob added to the
// schema and not to the struct is a control the settings form offers and nothing acts on — the
// worst kind of bug in a DKP system, because the officer sets it, believes it, and argues from it.
// A knob in the struct and not the schema is unreachable through the API and gets set only by an
// importer or a test, which is how two pools end up running different rules.

// schemaProperty is the part of a JSON Schema property this test compares.
type schemaProperty struct {
	Type    string          `json:"type"`
	Default json.RawMessage `json:"default"`
}

// parseFixedPriceSchema decodes the shipped schema into its properties.
func parseFixedPriceSchema(t *testing.T) map[string]schemaProperty {
	t.Helper()

	var doc struct {
		Type                 string                    `json:"type"`
		AdditionalProperties *bool                     `json:"additionalProperties"`
		Properties           map[string]schemaProperty `json:"properties"`
	}

	require.NoError(t, json.Unmarshal([]byte(fixedPriceConfigSchema), &doc),
		"the shipped config schema must be valid JSON — it is served to the SPA and to every SDK")

	require.Equal(t, "object", doc.Type)
	require.NotNil(t, doc.AdditionalProperties)
	require.False(t, *doc.AdditionalProperties,
		"additionalProperties must be false: a typo'd knob (decay_pb) has to be a validation error "+
			"at the edge, not a silently ignored key that leaves the pool running the default")
	require.NotEmpty(t, doc.Properties)

	return doc.Properties
}

// TestFixedPrice_ConfigSchema_DeclaresExactlyTheParsedKnobs closes the naming half of the gap.
func TestFixedPrice_ConfigSchema_DeclaresExactlyTheParsedKnobs(t *testing.T) {
	t.Parallel()

	schema := parseFixedPriceSchema(t)

	declared := make([]string, 0, len(schema))
	for name := range schema {
		declared = append(declared, name)
	}

	parsed := make([]string, 0, len(declared))

	typ := reflect.TypeOf(fixedPriceConfig{})
	for i := range typ.NumField() {
		tag, ok := typ.Field(i).Tag.Lookup("json")
		require.True(t, ok, "field %s has no json tag, so no schema property can name it",
			typ.Field(i).Name)

		parsed = append(parsed, tag)
	}

	sort.Strings(declared)
	sort.Strings(parsed)

	require.Equal(t, parsed, declared,
		"the config schema and fixedPriceConfig must name the same knobs. A knob only in the schema "+
			"is a control the settings form offers and the planner ignores; a knob only in the "+
			"struct is one the API cannot reach.")
}

// TestFixedPrice_ConfigSchema_DefaultsMatchTheParserDefaults closes the value half.
//
// The struct's defaults are what a pool that has set nothing actually runs under — the config JSON is
// decoded OVER defaultFixedPriceConfig(), so an absent key means the Go default and not the schema's.
// If the two disagree, the settings form shows one number and the ledger uses another, and the guild
// finds out from a balance.
func TestFixedPrice_ConfigSchema_DefaultsMatchTheParserDefaults(t *testing.T) {
	t.Parallel()

	schema := parseFixedPriceSchema(t)

	encoded, err := json.Marshal(defaultFixedPriceConfig())
	require.NoError(t, err)

	var applied map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &applied))

	for name, prop := range schema {
		require.NotNil(t, prop.Default,
			"knob %q declares no default, so the settings form has nothing to show for a pool that "+
				"has never set it", name)
		require.JSONEq(t, string(prop.Default), string(applied[name]),
			"knob %q defaults to %s in the schema and to %s in the parser",
			name, prop.Default, applied[name])
	}
}

// TestFixedPrice_ConfigSchema_MoneyKnobsAreIntegers restates canonical §1 where a schema could break
// it: `number` in a JSON Schema permits 12.5, and a decimal price is a float in the point path.
func TestFixedPrice_ConfigSchema_MoneyKnobsAreIntegers(t *testing.T) {
	t.Parallel()

	for name, prop := range parseFixedPriceSchema(t) {
		if prop.Type == "string" {
			continue
		}

		require.Equal(t, "integer", prop.Type,
			"knob %q is declared as %q. Money is integer centipoints (_cp) and ratios are integer "+
				"basis points (_bp); there is no decimal anywhere in the point path.", name, prop.Type)
	}
}
