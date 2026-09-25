---
description: the GT_LYDITE_RELAY fallback expression in reusable-lydite.yml and reusable-lydite-clearance.yml must stay identical — they are two hand-written copies, not one template.
---

# Rule: keep the relay fallback identical across reusable-lydite workflows

`.github/workflows/reusable-lydite.yml` and
`.github/workflows/reusable-lydite-clearance.yml` each pass
`relay: ${{ inputs.relay != '' && inputs.relay || vars.GT_LYDITE_RELAY }}` to
the lydite scan and clearance workflows they call. Nothing generates these
files from a shared template, so the two copies stay in sync only by
convention. If one is edited and the other is not, one job honors the
fleet-wide `GT_LYDITE_RELAY` variable and the other silently goes back to
requiring an explicit `relay` input — a split no test catches, because both
files are syntactically valid on their own.

## Applies to

`.github/workflows/reusable-lydite.yml` and
`.github/workflows/reusable-lydite-clearance.yml`, specifically the `relay:`
line passed to the reusable lydite workflows they call.

## Example

```yaml
# ✗ only reusable-lydite.yml gets the fallback; clearance quietly regresses
# reusable-lydite.yml
relay: ${{ inputs.relay != '' && inputs.relay || vars.GT_LYDITE_RELAY }}
# reusable-lydite-clearance.yml
relay: ${{ inputs.relay }}

# ✓ both files carry the same expression
relay: ${{ inputs.relay != '' && inputs.relay || vars.GT_LYDITE_RELAY }}
```
