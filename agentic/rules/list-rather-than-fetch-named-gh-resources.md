---
description: List and filter for a named live GitHub setting or resource instead of fetching it by name — GH.Run collapses "absent" and "broken credential" into the same error string.
---

# Rule: list and filter a named GitHub resource instead of fetching it by name

`GH.Run` collapses every non-zero `gh` exit into one error string. A `gh api
repos/<owner>/<repo>/actions/variables/<name>` for a variable that does not
exist and the same call against a broken token surface identically, so
reading "not found" out of that error treats a credential failure as a
repository that just needs writing to.

## Applies to

Any `internal/repogov` code (most concretely `settings.go`) that needs to
know whether a single named live GitHub setting or resource exists, wherever
"absent" has to be told apart from "the call itself failed."

## Example

`findLyditeRelayVar` lists `repos/%s/%s/actions/variables?per_page=100` and
filters client-side for `GT_LYDITE_RELAY`, rather than requesting
`.../actions/variables/GT_LYDITE_RELAY` directly and inferring absence from a
non-2xx response.
