# Saved searches

Users re-type the same filters every day. Let them save a search and rerun it from the board.

## Slices

### Store saved searches
Files: `internal/search/store.go`, `internal/search/store_test.go`
- A saved search persists its name and filter.
- Saving a name that exists replaces it.

### Run a saved search
Depends: Store saved searches
Files: `internal/search/run.go`
- Running a saved search returns the same rows as typing the filter.
- A missing name returns a clear error.

### Saved search CLI
Depends: Run a saved search
Files: `cmd/factory/cmd_search.go`
- `factory search save <name> <filter>` stores a search.
- `factory search run <name>` prints the rows.
- `factory search list` prints the names.
- `factory search rm <name>` removes one.
- Every command prints a one-line error for a missing name.
