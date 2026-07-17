# AGENTS.md — tinywasm/ddl

Working notes for AI agents operating in this library. For end-user docs see [README.md](README.md) and
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Some of this repo's algorithm used to live in
`tinywasm/orm`, before the storage contract was extracted to `tinywasm/storage` (the neutral DML port).

## Mission of this package

`tinywasm/ddl` is the **runtime DDL** counterpart of
[`tinywasm/storage`](https://github.com/tinywasm/storage) (the storage port — contract + DML value
types + conformance + mem + mock). [`tinywasm/orm`](https://github.com/tinywasm/orm) is a **sibling**,
not a dependency: both `ddl` and `orm` sit on top of `storage`, but neither imports the other. `ddl`
owns schema management: `CreateTable`/`DropTable`/`CreateDatabase`/`Sync`/`SyncSchema`, plus
`ddl/conformance` — the executable contract SQL backends (`sqlt`, `postgres`) prove themselves against,
mirroring `storage/conformance` for DML. `tinywasm/ddlc` stays the build-time codegen/CLI leaf
(`Exporter.ExportDDL`, `TopologicalSort`) — `ddl` is what *executes* the DDL that `ddlc` renders, at
runtime; `ddl` does not import `ddlc` as a library.

`ddl` depends on `storage` (`storage.Conn` for both `Exec` and, for `Sync`'s safe-drop probe, `Compile`
— see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)). **`ddl` never imports `orm`.** The dependency is
one-directional from `storage` outward: `storage` never imports `ddl` (or `orm`, or any backend).

## Architectural rules (do not violate)

### No Go `map` anywhere in this ecosystem

**Never use a built-in `map[K]V`, in any file, wasm-gated or not.** TinyGo's map runtime is heavy and
adds meaningful, unavoidable size to any wasm binary that ends up importing this code. This repo is
runtime code an executor adapter (`sqlt`, `postgres`) links into — including builds that end up in a
wasm target — so there is no "backend-only" exemption.

- For a **string→string** pair, use `github.com/tinywasm/fmt.KeyValue{Key, Value string}`.
- For anything else (typed values, a table's columns, a rename map, a PK set), use a small local
  slice-of-structs and scan it linearly. Every collection this package touches is small (a model's
  field list, one table's columns), so a linear scan costs nothing in practice. See
  `tinywasm/storage/mem`'s row/table pattern for the equivalent used elsewhere in the ecosystem.
- The `Sync` algorithm (`sync.go`) is map-free (`contains`/`schemaHasColumn`/`isRenameSource` helpers
  replace what would otherwise be `existingMap`/`schemaMap`/`renamedFrom`). The one exception:
  `RenameProvider.OldNames() map[string]string` is a **pre-existing external contract**
  (`ormc`-generated models across the ecosystem implement it) — this package only ever *reads* that
  caller-supplied map, it never allocates one of its own, so it doesn't violate the rule. Don't "fix"
  that signature here; it's out of scope and would ripple into `ormc` and every model that uses
  `db:"old_name=X"`.

### Root package (`ddl`) — orchestrates, never renders SQL

- **No direct SQL string building in `ddl`.** `ddl` decides *what* schema operation to run (`Stmt`/
  `Op`); the dialect's `Compiler.CompileDDL` decides *how* to render it as SQL. Don't hand-roll SQL
  strings here, even for something that looks trivial (e.g. `CREATE TABLE`).
- **No `database/sql` import.** Only `github.com/tinywasm/storage` (for `Conn`/`Executor`/`Compiler`/
  `Query`/`Condition`/`TxExecutor`), `github.com/tinywasm/model`, and `github.com/tinywasm/fmt`.
  **Never `github.com/tinywasm/orm`** — `ddl` and `orm` are siblings over `storage`, not a dependency
  of each other.
- **`ddl.DB` holds a `storage.Conn` + a `ddl.Compiler`, nothing else.** `storage.Conn` already unifies
  Executor+Compiler(DML), so `Sync`'s safe-drop probe uses `conn.Compile` directly — no separate DML
  compiler argument. `ddl.DB` implements its own transaction wrapping (`boundConn`, `sync.go`) — it
  isn't an `orm.DB` and doesn't call anything from `orm`.
- **Do not use `tinygo` as a build tag** — not a real Go build constraint. Use `GOOS=js GOARCH=wasm` to
  build for wasm, `gotest -tinygo` to test against the TinyGo compiler specifically.

## Code layout

| File / Dir | Role |
|------------|------|
| `compiler.go` | `Op`, `Stmt` (incl. `ColumnName` for `OpDropColumn`), `Compiler` (DDL) — dialect adapters implement `CompileDDL` |
| `db.go` | `ddl.DB`, `New(conn storage.Conn, ddlCompiler Compiler)`, `CreateTable`/`DropTable`/`CreateDatabase` |
| `sync.go` | `Sync`/`SyncSchema` algorithm (map-free — see rule above), `boundConn` for transactions |
| `schema.go` | `TableIntrospector`, `SchemaInspector`, `ColumnInfo` |
| `conformance/` | Executable DDL contract (`Run(t, Factory)`), SQL backends only |
| `docs/` | `ARCHITECTURE.md` (durable design), `PLAN.md` when a new change is being planned (ephemeral, deleted after `gopush`) |

## Testing

Install once:

```bash
go install github.com/tinywasm/devflow/cmd/gotest@latest
```

Run:

```bash
gotest              # vet + race + cover + wasm + badges
gotest -no-cache    # force re-run
gotest -run TestX   # filter
```

Publish with `gopush 'message'` (tests + tag + push) — never `git commit`/`git push` directly.

## Common mistakes to avoid

- Reaching for `map[K]V` for a lookup table, a rename map, or a PK set → use `fmt.KeyValue` or a small
  local slice-of-structs scanned linearly instead. No exceptions.
- Rendering SQL directly in `ddl` instead of going through the dialect's `Compiler.CompileDDL`.
- Importing `github.com/tinywasm/orm` for anything → `ddl` depends on `storage`, never on `orm`. If you
  find yourself wanting `orm.X`, the type you need is `storage.X`.
- Adding a third constructor argument (a separate DML compiler) to `ddl.New` → `storage.Conn` already
  carries `Compile`, that's the DML compiler. Two arguments (`conn`, `ddlCompiler`) is correct.
