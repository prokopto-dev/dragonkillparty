package repogate

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// The catalogue loader: rules.hcl in, []textRule out.
//
// IT FAILS CLOSED, and that is the only property of this file that matters. Everything the engine
// does about laws 1-4, the money rules, the design tokens, the supply-chain pins and the AGPL
// firewall depends on a catalogue arriving intact; a decoder that returned an empty slice on a file
// it could not read would disable every one of them AND PRINT `repo gates passed`. So there is
// exactly one shape of failure here — an error, which runTextRules turns into GATE000 — and no path
// that yields fewer rules than the file declares. A catalogue with no rules in it is an error too:
// an empty file is far more likely to be a mistake than a decision.
//
// The catalogue is EMBEDDED rather than read from the tree under inspection. DKP_REPO_ROOT points at
// the tree being INSPECTED, which for a negative fixture is a t.TempDir() with nothing in it, and a
// gate engine that read its own rules out of that tree would be one a tainted tree could disarm.
//
//go:embed rules.hcl
var ruleCatalogue []byte

// ruleCatalogueRel is what a decode failure names. Repo-relative, because that is the file to open.
const ruleCatalogueRel = "internal/repogate/rules.hcl"

// ruleFile is rules.hcl's schema, and gohcl enforces it: an attribute this struct does not declare
// is a decode error rather than a silently ignored line. That is deliberate — a typo'd `patern` in a
// rule would otherwise leave the rule with no pattern at all.
type ruleFile struct {
	Rules []ruleBlock `hcl:"rule,block"`
}

// ruleBlock is one (rule, tree) pair as written in the catalogue. The fields are textRule's, in the
// same order, so the two can be read side by side.
type ruleBlock struct {
	ID          string   `hcl:"id,label"`
	Description string   `hcl:"description"`
	Tree        string   `hcl:"tree"`
	Pattern     string   `hcl:"pattern"`
	Extra       []string `hcl:"extra,optional"`
	Include     []string `hcl:"include,optional"`
	Reject      []string `hcl:"reject,optional"`
	Strip       []string `hcl:"strip,optional"`
	Quiet       bool     `hcl:"quiet,optional"`
}

// textRules decodes the embedded catalogue.
//
// It is called once per run rather than cached in a package-level variable: the cost is a parse of
// one small file, and package-level mutable state is what `-shuffle=on` plus `t.Parallel()` turn
// into an intermittent failure (.claude/rules/go-idioms.md).
func textRules() ([]textRule, error) {
	return decodeRules(ruleCatalogue)
}

// decodeRules parses one catalogue and compiles every pattern in it.
func decodeRules(src []byte) ([]textRule, error) {
	file, diags := hclsyntax.ParseConfig(src, ruleCatalogueRel, hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse %s: %w", ruleCatalogueRel, diags)
	}

	var decoded ruleFile

	if diags := gohcl.DecodeBody(file.Body, nil, &decoded); diags.HasErrors() {
		return nil, fmt.Errorf("decode %s: %w", ruleCatalogueRel, diags)
	}

	if len(decoded.Rules) == 0 {
		return nil, fmt.Errorf("%s declares no rules", ruleCatalogueRel)
	}

	rules := make([]textRule, 0, len(decoded.Rules))

	for _, blk := range decoded.Rules {
		rule, err := blk.compile()
		if err != nil {
			return nil, err
		}

		rules = append(rules, rule)
	}

	return rules, nil
}

// compile turns one declared rule into the runnable one.
func (b ruleBlock) compile() (textRule, error) {
	if b.ID == "" || b.Description == "" || b.Tree == "" {
		return textRule{}, fmt.Errorf("%s: a rule needs an id, a description and a tree", ruleCatalogueRel)
	}

	pattern, err := compilePattern(b.ID, b.Pattern)
	if err != nil {
		return textRule{}, err
	}

	reject := make([]*regexp.Regexp, 0, len(b.Reject))

	for _, raw := range b.Reject {
		re, err := compilePattern(b.ID, raw)
		if err != nil {
			return textRule{}, err
		}

		reject = append(reject, re)
	}

	return textRule{
		id: b.ID, desc: b.Description,
		tree: b.Tree, extra: b.Extra, include: b.Include,
		pattern: pattern, reject: reject,
		strip: b.Strip, quiet: b.Quiet,
	}, nil
}

// compilePattern compiles one pattern from the catalogue.
//
// The surrounding whitespace is trimmed because every pattern is written as a heredoc — HCL does not
// process backslash escapes inside one, which is what lets a regexp be written here exactly as it
// would be in Go — and a heredoc carries the newline that ends its last line. No regexp in this
// catalogue ends in whitespace, and one that needed to would have to say so as `\s` rather than by
// trailing a space nobody can see in a diff.
func compilePattern(id, raw string) (*regexp.Regexp, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("%s: rule %s has an empty pattern", ruleCatalogueRel, id)
	}

	re, err := regexp.Compile(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%s: rule %s: compile %q: %w", ruleCatalogueRel, id, trimmed, err)
	}

	return re, nil
}

// errNoCatalogue is what a caller sees when the catalogue could not be loaded at all. It exists so
// that "the rules could not be read" is a distinct outcome from "a rule fired", in the engine as
// well as in the exit code.
var errNoCatalogue = errors.New("the repository gate catalogue could not be read")
