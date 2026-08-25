---
PLAN: "feat!: ddl.New accepts Execer — a DDL-only connection no longer has to fake Query"
EXECUTOR: jules
REVIEWER: none
STATUS: running
SESSION: 12032918283376205578
---

> This plan is dispatched via the CodeJob workflow. See skill: agents-workflow.
>
> Read `AGENTS.md` first. **This plan deliberately changes one of its stated
> architectural rules** — see "The rule this changes" below. That is the
> point of the change, not an oversight; Stage 4 updates the rule.

# Plan — `tinywasm/ddl`: require only what `Sync` actually uses

## Why

`ddl.New` demands a full `storage.Conn` (`Exec` + `QueryRow` + `Query` +
`Close` + `Compile`). `Sync` does not need most of it. Reading `sync.go`:

- `CreateTable`, `DropTable`, `CreateDatabase`, `execDDL` → `d.conn.Exec` only.
- `syncModel` step 2 **returns early** when the connection does not implement
  `TableIntrospector`, taking a purely additive `Exec`-only path.
- `d.conn.Compile` / `d.conn.Query` (sync.go:180–185) are reached **only** in
  step 6 ("Reconcile Safe Drops"), which is downstream of that early return.
- `storage.TxExecutor`, `TableIntrospector` and `SchemaInspector` are already
  *optional*, discovered by type assertion — not part of the declared
  parameter.

So a connection that can only execute DDL — the exact shape needed to run a
migration against Cloudflare D1's HTTP API from CI, which has no `Query`
endpoint semantics worth implementing — cannot be expressed today. It must
declare `Query` and `QueryRow` and return errors from them. That is a
runtime lie the type system should have caught:
`app-releases/docs/CONSTRUCTION_HARNESS.md` names it twice — *"Illegal states
unrepresentable"* and *"A missing contract at a boundary is a defect in the
library, not in the consumer."*

The consumer that surfaced it is
`tinywasm/cloudflare`'s planned `d1.NewMigrator` (see that repo's
`docs/PLAN.md`), which is blocked on this change and must not ship the faked
methods.

## This is not a breaking change for existing callers

`Execer` is **narrower** than `storage.Conn`, so accepting it *widens* what
`New` takes. Every current caller passes a `storage.Conn`, and every
`storage.Conn` has `Exec` — so `postgres`, `sqlite`, `orm`, `goflare/d1` and
the conformance suite keep compiling untouched. Nothing that works today
stops working.

The `!` in the plan title marks the **documented-rule** change (Stage 4), not
a source break. Verify that claim rather than trusting it: Stage 5 requires
building at least one real consumer against the new version.

## The rule this changes

`AGENTS.md` currently states, under "Root package (`ddl`) — orchestrates,
never renders SQL":

> **`ddl.DB` holds a `storage.Conn` + a `ddl.Compiler`, nothing else.**
> `storage.Conn` already unifies Executor+Compiler(DML), so `Sync`'s
> safe-drop probe uses `conn.Compile` directly — no separate DML compiler
> argument.

The rule's *intent* — don't proliferate constructor arguments, don't take a
separate DML compiler — is preserved exactly: `New` still takes two
arguments and there is still no DML-compiler parameter. What changes is only
the *declared minimum* of the first one. Stage 4 rewrites the rule to say
that, so the file stops describing a design the code no longer has.

## Stage 1 — `Execer` and the widened `New` (`db.go`)

```go
// Execer is the only capability ddl.DB always needs from its connection:
// somewhere to send compiled DDL. Everything else Sync can use —
// storage.Compiler and Query for the safe-drop probe, storage.TxExecutor for
// transactional sync, TableIntrospector/SchemaInspector for reconciliation —
// is optional and discovered by type assertion, so requiring the full
// storage.Conn here excluded connections that legitimately execute DDL and
// nothing else (a CI migration transport over an HTTP API, for instance).
// storage.Conn satisfies this interface, so every existing caller is
// unaffected.
type Execer interface {
	Exec(query string, args ...any) error
}

// DB applies schema changes through an Execer: Exec for the compiled DDL.
// When the connection also satisfies storage.Compiler and exposes Query,
// Sync additionally runs its safe-drop probe (see sync.go); when it does
// not, Sync takes the additive path that needs neither.
type DB struct {
	conn        Execer
	ddlCompiler Compiler
	log         func(...any)
}

func New(conn Execer, ddlCompiler Compiler) *DB {
	return &DB{conn: conn, ddlCompiler: ddlCompiler}
}
```

`CreateTable`, `DropTable` and `CreateDatabase` are unchanged — they already
call only `d.conn.Exec`.

## Stage 2 — guard the two optional uses (`sync.go`)

Three places assume more than `Execer` and must now assert. **Do not change
what any of them does when the capability is present** — only add the
guard.

**(a) `Sync`, ~line 49** — `boundConn{TxBoundExecutor: bound, Compiler: d.conn}`
embeds `d.conn` as a `storage.Compiler`. That whole transactional branch is
already behind `d.conn.(storage.TxExecutor)`; it needs the Compiler half too:

```go
	txExec, ok := d.conn.(storage.TxExecutor)
	dmlCompiler, hasCompiler := d.conn.(storage.Compiler)
	if !ok || !hasCompiler {
		return d.syncAll(models...)
	}
```

and every `Compiler: d.conn` inside that branch becomes
`Compiler: dmlCompiler`. A connection that can begin a transaction but
cannot compile DML has no safe-drop probe to run, so falling back to
`syncAll` is correct — not a silent downgrade, because `syncAll` is the same
function the non-transactional path already uses.

**(b) `syncModel` step 6, ~lines 180–185** — the safe-drop probe. Guard the
whole loop, not each call:

```go
	// 6. Reconcile Safe Drops — needs the DML half (Compile + Query) to ask
	// whether a column about to be dropped still holds data. A connection
	// without it (a DDL-only migration transport) skips the drop rather than
	// dropping blind: losing a populated column is unrecoverable, and no
	// probe means no evidence it is empty.
	prober, canProbe := d.conn.(interface {
		storage.Compiler
		Query(query string, args ...any) (storage.Rows, error)
	})
	if !canProbe {
		if len(existingCols) > 0 {
			d.logw("sync:", tableName, "safe drop skipped: connection cannot probe for data")
		}
		return nil
	}
```

then `d.conn.Compile(...)` → `prober.Compile(...)` and `d.conn.Query(...)` →
`prober.Query(...)` inside the existing loop body.

Read the real file before editing: the line numbers above are from
2026-08-25 and the surrounding logic (rename handling, `oldNames`,
`logw` calls) must be preserved exactly.

## Stage 3 — tests

Add to the existing suite (`ddl_test.go`, which already has
`TestSync_NoIntrospector`, `TestSync_Transaction`, etc. — follow their
fixture style, do not invent a new harness):

1. **`TestNew_AcceptsDDLOnlyConn`** — a fake implementing *only*
   `Exec(string, ...any) error` plus a `Compiler`, passed to `ddl.New`, and
   `Sync` over one model succeeds. This is the test that fails to compile
   today, which is the whole point: assert it now compiles and runs.
2. **`TestSync_DDLOnlyConn_SkipsSafeDrop`** — same fake, but the model omits
   a column the (fake) table has. Assert no `OpDropColumn` statement is
   emitted and that `Exec` still received the `CreateTable`/`AddColumn`
   statements. Record the statements the fake receives in a slice — **no
   `map`** (`AGENTS.md`).
3. **Existing behavior unchanged** — `TestSync_WithIntrospector_SafeDrop_WithData`
   and `..._NoData` must still pass untouched. If either needs editing, the
   guard in Stage 2(b) is wrong: stop and re-read it.

## Stage 4 — `AGENTS.md`

Replace the bullet quoted in "The rule this changes" with one that describes
the design as it now is: `ddl.DB` holds an `Execer` + a `ddl.Compiler`;
`storage.Compiler`/`Query`/`TxExecutor`/`TableIntrospector`/`SchemaInspector`
are all optional capabilities discovered by assertion; there is still no
separate DML-compiler argument. Keep the rule's original intent visible — a
future reader must not "restore" `storage.Conn` thinking the narrowing was
an accident.

## Stage 5 — verify the no-break claim

Do not merge on the reasoning in "This is not a breaking change" alone.
Build at least one real consumer against the modified `ddl` via a local
`replace` and confirm it compiles and its tests pass — `tinywasm/sqlite` is
the cheapest (pure Go, no external service). `tinywasm/postgres` needs a
live database, so its conformance run is optional; compiling it is not.

## Acceptance criteria

- [ ] `go build ./...` and `go vet ./...` clean.
- [ ] `gotest` green — the full existing suite, plus the two new tests, with
      no edits to the existing safe-drop tests.
- [ ] `grep -n "storage.Conn" db.go` → no match in the `DB` struct or in
      `New`'s signature (the doc-comment mentions may remain if accurate).
- [ ] `grep -rn "map\[" *.go | grep -v OldNames` → empty (unchanged rule).
- [ ] A local `replace` build of `tinywasm/sqlite` against this version
      compiles and its tests pass (Stage 5).
- [ ] `AGENTS.md` no longer claims `ddl.DB` holds a `storage.Conn`.

| Stage | File(s) | Done when |
|---|---|---|
| 1 | `db.go` | `Execer` declared; `New` and `DB.conn` take it |
| 2 | `sync.go` | Transactional branch and safe-drop probe guarded by assertion; behavior identical when the capability is present |
| 3 | `ddl_test.go` | DDL-only conn compiles, syncs, and skips safe-drop; existing tests untouched and green |
| 4 | `AGENTS.md` | The `storage.Conn` rule rewritten to match the code |
| 5 | (verification) | A real consumer builds and tests green against this change |
