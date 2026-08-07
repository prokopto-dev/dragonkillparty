package api

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// specIndent is the indentation of the committed openapi/openapi.json.
//
// Indented rather than compact because the file is committed and reviewed: a one-line JSON document
// makes `git diff` on a spec change useless, and the spec diff is the thing a reviewer most needs to
// see on an API PR.
const specIndent = "  "

// SpecJSON returns the OpenAPI 3.1 document exactly as it is committed to openapi/openapi.json,
// trailing newline included.
//
// BYTE STABILITY IS THE CONTRACT, not a nicety. `make verify-generated` hashes the openapi tree,
// regenerates, and hashes again, so any run-to-run variation reports drift on a tree nobody touched
// and blocks every PR until someone works out why. Three things make it stable:
//
//   - Huma marshals each object by building a map[string]any and handing it to encoding/json, which
//     sorts map keys (huma/v2 openapi.go marshalJSON). Key order is therefore alphabetical and
//     independent of Go's map iteration order.
//   - Nothing in the document comes from Config. The build stamps, the clock and the readiness
//     checker are runtime values; info.version is SpecVersion, a constant.
//     TestOpenAPI_ConfigVariation_ProducesIdenticalSpec holds that line.
//   - json.Indent rewrites whitespace only, preserving the order above.
//
// The trailing newline is part of the bytes. A file without one is a POSIX text-file violation that
// makes `git diff` print "\ No newline at end of file" on every change, and `dkp openapi > file`
// has to produce the file byte for byte.
func SpecJSON() ([]byte, error) {
	doc, err := NewHumaAPI(Config{}).OpenAPI().MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal openapi document: %w", err)
	}

	var out bytes.Buffer
	if err := json.Indent(&out, doc, "", specIndent); err != nil {
		return nil, fmt.Errorf("indent openapi document: %w", err)
	}

	out.WriteByte('\n')

	return out.Bytes(), nil
}
