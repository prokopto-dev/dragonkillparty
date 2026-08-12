package mockup

import (
	"bytes"
	"fmt"

	"golang.org/x/net/html"
)

const bannerStyle = `<style id="dkp-mockup-banner-style">
body{padding-bottom:38px}
sc-if,sc-for{display:contents}
#dkp-mockup-banner{position:fixed;left:0;right:0;bottom:0;z-index:2147483647;
  display:flex;align-items:center;gap:10px;flex-wrap:wrap;
  padding:8px 16px;font:500 12px/1.3 Inter,system-ui,sans-serif;
  color:#e9e9ed;background:#2b2741;border-top:1px solid #5d5294;
  box-shadow:0 -6px 18px rgba(0,0,0,.45)}
#dkp-mockup-banner strong{letter-spacing:.14em;font-size:10px;color:#161826;
  background:#b5abfc;border-radius:4px;padding:3px 7px}
#dkp-mockup-banner span{opacity:.72}
#dkp-mockup-banner .dkp-mockup-surface{margin-left:auto;opacity:.5}
#dkp-mockup-banner a{color:#d2cefd;text-decoration:none;
  border-bottom:1px solid rgba(210,206,253,.35)}
#dkp-mockup-banner a:hover{color:#f5f4ff}
@media print{#dkp-mockup-banner{display:none}}
</style>`

const bannerTemplate = `<div id="dkp-mockup-banner">` +
	`<strong>MOCKUP</strong>` +
	`<span>&mdash; not a live instance. Static design reference; nothing here is wired ` +
	`to a server.</span>` +
	`<span class="dkp-mockup-surface">%s</span>` +
	`<a href="./index.html">All surfaces</a>` +
	`<a href="https://github.com/prokopto-dev/dragonkillparty">Repository</a>` +
	`</div>`

// robotsMeta is the noindex every surface carries.
//
// The mockups are fabricated guild data for an unreleased product, and a stray search result would
// read as a live instance — the banner says "not a live instance", but a search snippet does not
// show the banner.
//
// It has to be per page. index.html has had this by hand since the site was created, but noindex
// does not propagate to the pages it links to, so the five surfaces were indexable. A robots.txt
// cannot cover them either: Pages serves this repo as a *project* site under /dragonkillparty/, and
// crawlers only read robots.txt at the origin root — which belongs to a different repository. Nor
// can we set X-Robots-Tag, since Pages does not let us add response headers. The meta tag is the
// only mechanism available here, which is why MOCK004 enforces it rather than trusting it.
const robotsMeta = `<meta name="robots" content="noindex">`

// injectChrome performs steps 6 and 7: the banner (plus the styles that keep it clear of the
// mockups' own sticky chrome) and the noindex.
//
// Anchored on the parsed </head> and <body> tokens rather than on a substring search. The Python
// had to check for the absence of each anchor by hand, because str.replace returns the string
// unchanged when there is no match and would have dropped the banner and the noindex silently; here
// a missing anchor simply is not in the token stream, and saying so is the only option.
func injectChrome(name string, toks []token, title string) error {
	head, body := -1, -1

	for i, t := range toks {
		if head == -1 && t.typ == html.EndTagToken && t.name == "head" {
			head = i
		}

		if body == -1 && t.typ == html.StartTagToken && t.name == "body" {
			body = i
		}
	}

	if head == -1 {
		return fmt.Errorf("%s: no </head> — the noindex and the banner styles have nowhere to go", name)
	}

	if body == -1 {
		return fmt.Errorf("%s: no <body> — the mockup banner has nowhere to go", name)
	}

	toks[head].raw = bytes.Join([][]byte{
		[]byte(robotsMeta),
		[]byte(bannerStyle),
		toks[head].raw,
	}, []byte("\n"))

	banner := fmt.Sprintf(bannerTemplate, html.EscapeString(title))
	toks[body].raw = append(append(bytes.Clone(toks[body].raw), '\n'), banner...)

	return nil
}
