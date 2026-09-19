# Website design and maintenance

## Scope

The website is a presentation layer over the existing TPA Hub routes and
contracts. Template and stylesheet changes should remain separate from product
behavior, storage, authentication, and deployment changes.

Public pages answer distinct questions:

- `/`: what the product does and where to start;
- `/pricing`: current capacity, future plans, and payment availability;
- `/nano`: when small packages or optional recipes are a good fit;
- `/docs`: installation, trust, compatibility, consumption, publication, and limits;
- `/account`: actual available usage data and conditional test checkout;
- `/ops/*`: operator-only workflows with their own navigation;
- unknown website routes: a shared, accessible 404 page.

## Shared implementation

`cmd/tpahub/templates/layout.html` owns the head, public navigation, operator
header, footer, Nano resource summary, and onboarding release notice.
`cmd/tpahub/static/site.css` owns design tokens and components. No frontend
framework, external font, CDN, or new runtime JavaScript dependency is needed.

Use semantic sections, one h1 and one main per page, explicit form labels, and
visible focus indicators. Keep command blocks scrollable without making the
whole page overflow. Data tables belong inside keyboard-focusable scroll regions.
The public navigation remains consistent even on login and account pages; the
Account link uses the existing authenticated route and its login redirect.

Keep plan capacity copy in its shared partial. Do not duplicate pricing cards
onto the homepage or append roadmap paragraphs to the installation guide.
Sandbox amounts must be labelled as test-only; future plans must not have buy
buttons. Compatibility notices must be reviewed when the public CLI is released.
These are editorial statements, not a new persistent source of billing authority.

Existing form methods, field names, endpoints, operator JS hooks and authorization
predicates are retained. Bootstrap rendering moves from string concatenation to
an autoescaped template; its authorization and mutation path are not changed.
The 404 renderer still returns 404, and repository/API error handling is unchanged.

## Validation

Production-router tests cover the shared shell, authenticated account/operator
pages, unauthenticated redirects, form contracts, and escaped error messages.
Existing auth, operator, resource formatting and Bootstrap tests remain relevant.

```sh
go test ./cmd/tpahub -run 'TestWebsite|TestCentralLegal|TestOps|TestOperator|TestBootstrap'
go test -race ./cmd/tpahub -run 'TestWebsite|TestCentralLegal|TestOps|TestOperator|TestBootstrap'
go vet ./...
node --test tests/ops-resource-test.js
```

The full `go test ./...` suite is expected to pass. UI assertions belong in
semantic website/layout tests; they should not require the previous page copy or
HTML nesting.

For browser checks, build and run the ordinary server with a disposable local
data directory (never point the preview at production data):

```sh
go build -o /tmp/tpahub-website-preview ./cmd/tpahub
TPAHUB_LISTEN=127.0.0.1:18093 TPAHUB_DATA_DIR="$(mktemp -d)" \
  /tmp/tpahub-website-preview serve
```

In another terminal, with Firefox installed:

```sh
python3 -m venv /tmp/tpahub-website-browser
/tmp/tpahub-website-browser/bin/pip install selenium==4.49.0
/tmp/tpahub-website-browser/bin/python tests/website-browser.py \
  --screenshots /tmp/tpahub-website-screenshots
```

The script only accepts a localhost preview. It does not submit forms. Firefox
BiDi sets and verifies actual 360, 768 and 1440 CSS-pixel viewports (ordinary
window resizing can silently clamp Firefox to a larger minimum). It checks nine
public/operator-entry/error pages for overflow, heading/landmark counts, labels,
fragment targets, keyboard skip navigation, and unindented command examples, and
captures full-page screenshots. Authenticated layouts are covered by router tests,
not claimed as browser-tested. This is a smoke test, not a complete accessibility
audit.
