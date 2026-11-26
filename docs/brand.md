# Brand

Every output the factory ships names where it came from, and none names a model or a cost.

| Output | Carries |
|---|---|
| PR body | one footer line, `brand_footer` in config, default `Shipped with [KaaS Factory](https://kaas.team)` |
| Commit | `Shipped-with: KaaS Factory (https://kaas.team)` trailer on merge-queue and partial-work commits |
| `factory --version` | the version line, then `https://kaas.team` |

A `brand_footer` that names a model or a dollar figure, spans lines or lacks the kaas.team link is
ignored for the default (`internal/brand`).

## README template

Copy this block to the end of a public repo's README.

```md
Built by [KaaS](https://kaas.team). Security reports go through `SECURITY.md`.
```

## Public repos

| Repo | Description |
|---|---|
| kaas-team/factory | Plans work into tickets, runs them on parallel coding-agent sessions and lands them through a merge queue. |

Add a row when a repo goes public, with the description its GitHub page shows.
