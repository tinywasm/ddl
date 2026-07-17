---
PLAN: "feat: tinywasm/ddl — runtime DDL (repo nuevo) + ddl/conformance, sobre tinywasm/storage"
TAG: v0.0.1
---

> **Prerequisito (entorno del agente):**
> ```bash
> go install github.com/tinywasm/devflow/cmd/gotest@latest
> ```
> Tests SIEMPRE con `gotest`. Publica SIEMPRE con `gopush 'mensaje'` (no `git commit`/`git push`).
> Este plan **requiere `github.com/tinywasm/storage@v0.0.1` ya publicado** — si no resuelve en `go get`,
> para y repórtalo, no repliques su contrato aquí.

## 0. Corrección 2026-07-16 (tercera pasada) — `ddl` depende de `storage`, no de `orm`

**Cambio respecto a las dos versiones anteriores de este plan.** Primero se decidió romper `orm` de
una vez (DML-puro) en vez de una migración gradual. Después surgió una pregunta más de fondo: si `orm`
seguía siendo dueño del contrato de almacenamiento, todo backend seguía dependiendo del ORM completo
solo para cumplir interfaces. La respuesta fue extraer el contrato mismo a un puerto neutral,
`tinywasm/storage` (razonamiento completo en
[`DB_PORT_PROPOSAL.md`](https://github.com/tinywasm/app-releases/blob/main/docs/DB_PORT_PROPOSAL.md) —
no hace falta leerlo para ejecutar este plan, es autocontenido). Consecuencias para `ddl`:

- **`ddl` depende de `storage`, nunca de `orm`.** `orm` es ahora una capa ergonómica opcional que ni
  siquiera sabe que `ddl` existe. Si en algún punto de este documento ves `orm.X`, es un rastro de una
  versión vieja — repórtalo, no lo repliques.
- **`ddl.New` toma DOS argumentos, no tres.** La versión anterior de este plan pedía
  `New(exec orm.Executor, ddlCompiler ddl.Compiler, dmlCompiler orm.Compiler)` porque `orm.DB` guardaba
  el executor y el compilador DML por separado. `storage.Conn` (el puerto nuevo) ya **une** ambos en un solo
  valor — así que `ddl.New(conn storage.Conn, ddlCompiler ddl.Compiler)` alcanza: `conn` sirve de executor
  **y** de compilador DML (para el safe-drop de `Sync`, que sigue siendo una lectura DML real, no DDL).
  Ver §3.2.
- **Gap de diseño corregido en esta pasada:** `ddl.Stmt` (§3.1) no tenía forma de expresar "elimina
  esta columna por nombre" sin inventarse un `model.Field` falso — el algoritmo original de `Sync`
  usaba `Query{Columns: []string{col}}` (un slice de nombres) para `DropColumn`, pero `Stmt.Column` es
  un `*model.Field` (pensado para `AddColumn`/`RenameColumn`, que sí necesitan tipo). Se añade
  `Stmt.ColumnName string`, usado solo por `OpDropColumn`. Ver §3.1.
- El resto del diseño (`RenameProvider`, `TableIntrospector`, el algoritmo de safe-drop, las 12
  cláusulas... perdón, las 4 cláusulas de `ddl/conformance`) no cambia de intención — solo de qué
  paquete provienen los tipos.

## 1. Qué es y por qué

`tinywasm/orm` mezclaba DML (operar datos) y DDL (crear/migrar esquema) en el mismo `*orm.DB`. Ese
contrato completo (DML+DDL) se extrajo primero a `orm` (DML) + este repo (DDL), y después el contrato
DML mismo se extrajo de `orm` a `tinywasm/storage` — el puerto neutral que ningún backend debería rodear
importando un ORM. Este repo, `tinywasm/ddl`, **es el runtime de DDL**: absorbe toda la superficie de
esquema y añade su propio contrato ejecutable `ddl/conformance`. `tinywasm/ddlc` **no** cambia: sigue
siendo la CLI/codegen leaf que **genera** el SQL DDL (`Exporter.ExportDDL`) — `ddl` la **ejecuta** en
runtime.

**Alcance de este plan: SOLO `tinywasm/ddl`.** No toques `tinywasm/storage`, `tinywasm/orm`, ni ningún
backend.

## 2. Código de referencia (el algoritmo que hay que portar — ya no vive en ningún repo tal cual,
   inlineado aquí para que el plan sea autocontenido)

### 2.1 Métodos DDL que antes vivían en `orm.DB` (ahora no existen en ningún repo — repórtalos)

```go
// CreateTable creates a new table for the given model.
func (d *DB) CreateTable(m model.Model) error {
	q, args, err := d.ddlCompiler.CompileDDL(Stmt{Op: OpCreateTable, Table: m.ModelName()}, m)
	if err != nil {
		return err
	}
	return d.conn.Exec(q, args...)
}

// DropTable drops the table for the given model.
func (d *DB) DropTable(m model.Model) error {
	q, args, err := d.ddlCompiler.CompileDDL(Stmt{Op: OpDropTable, Table: m.ModelName()}, m)
	if err != nil {
		return err
	}
	return d.conn.Exec(q, args...)
}

// CreateDatabase creates a new database. No model.Model needed — OpCreateDatabase only carries
// the database name.
func (d *DB) CreateDatabase(name string) error {
	q, args, err := d.ddlCompiler.CompileDDL(Stmt{Op: OpCreateDatabase, Database: name}, nil)
	if err != nil {
		return err
	}
	return d.conn.Exec(q, args...)
}
```

> A diferencia del `orm.DB` original, no hace falta un `emptyModel` sentinel para `CreateDatabase`:
> `CompileDDL` recibe `m model.Model` como segundo argumento igual que los demás casos, y para
> `OpCreateDatabase` el dialecto simplemente lo ignora (pásale `nil`). Más simple que replicar el
> sentinel — no lo repliques.

### 2.2 Introspección de esquema (íntegro, sin cambios de forma respecto a versiones previas)

```go
// TableIntrospector is optionally implemented by the injected storage.Conn to retrieve column names.
type TableIntrospector interface {
	TableColumns(table string) ([]string, error)
}

// SchemaInspector is optionally implemented by storage.Conn to expose broader schema introspection.
// If the adapter does not implement it, the db_schema MCP tool is not registered.
type SchemaInspector interface {
	Tables() ([]string, error)
	Columns(table string) ([]ColumnInfo, error)
}

// ColumnInfo describes a single column returned by SchemaInspector.
type ColumnInfo struct {
	Name    string
	Type    string
	NotNull bool
	PK      bool
}
```

### 2.3 El algoritmo `Sync`/`SyncSchema` (íntegro, ya traducido a `storage.Conn` + `Stmt`/`Op`)

```go
// RenameProvider is implemented by generated models when db:"old_name=X" tags are present.
type RenameProvider interface {
	OldNames() map[string]string
}

// SyncSchema reconciles one table to the given fields, with no model instance.
func (d *DB) SyncSchema(table string, fields []model.Field) error {
	m := &schemaModel{name: table, fields: fields}
	return d.Sync(m)
}

type schemaModel struct {
	name   string
	fields []model.Field
}

func (s *schemaModel) ModelName() string             { return s.name }
func (s *schemaModel) Schema() []model.Field         { return s.fields }
func (s *schemaModel) Pointers() []any               { return nil }
func (s *schemaModel) IsNil() bool                   { return s == nil }
func (s *schemaModel) EncodeFields(model.FieldWriter) {}
func (s *schemaModel) DecodeFields(model.FieldReader) {}

// Sync reconciles the database to match the given models.
func (d *DB) Sync(models ...model.Model) error {
	if len(models) == 0 {
		return nil
	}
	txExec, ok := d.conn.(storage.TxExecutor)
	if !ok {
		return d.syncAll(models...)
	}
	bound, err := txExec.BeginTx()
	if err != nil {
		return err
	}
	// boundConn re-pairs the transaction-bound Executor with the original connection's
	// Compiler (compiling doesn't depend on being inside a transaction, only executing
	// does) — same pattern orm.DB.Tx uses for the exact same reason, see
	// https://github.com/tinywasm/orm/blob/main/docs/PLAN.md §4.3. ddl doesn't import orm,
	// so this is a small independent copy of the same idea, not a shared type.
	txDB := &DB{conn: boundConn{TxBoundExecutor: bound, Compiler: d.conn}, ddlCompiler: d.ddlCompiler, log: d.log}
	if err := txDB.syncAll(models...); err != nil {
		bound.Rollback()
		return err
	}
	return bound.Commit()
}

// boundConn satisfies storage.Conn by composing a transaction-bound Executor with the original
// connection's Compiler half.
type boundConn struct {
	storage.TxBoundExecutor
	storage.Compiler
}

func (d *DB) syncAll(models ...model.Model) error {
	for _, m := range models {
		if err := d.syncModel(m); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) syncModel(m model.Model) error {
	tableName := m.ModelName()
	schema := m.Schema()

	// 1. Emit OpCreateTable (idempotent)
	if err := d.CreateTable(m); err != nil {
		return wrapSyncErr(err)
	}

	// 2. Cast conn to TableIntrospector
	introspector, ok := d.conn.(TableIntrospector)
	if !ok {
		// Fallback to purely additive loop
		for _, field := range schema {
			if err := d.execDDL(Stmt{Op: OpAddColumn, Table: tableName, Column: &field}, m); err != nil {
				d.logw("sync:", tableName, "add column", field.Name, "skipped:", err)
			}
		}
		return nil
	}

	// 3. Retrieve existing columns
	existingCols, err := introspector.TableColumns(tableName)
	if err != nil {
		return wrapSyncErr(err)
	}

	// 4. Check if model implements RenameProvider. OldNames() returns map[string]string — a
	// pre-existing external contract (ormc-generated models implement it), not a map this
	// package allocates itself, so the no-map rule (AGENTS.md) doesn't reach it: we only ever
	// read from the caller-supplied map, never build one of our own.
	var oldNames map[string]string
	if rp, ok := m.(RenameProvider); ok {
		oldNames = rp.OldNames()
	}

	// 5. Reconcile Adds & Renames
	for _, field := range schema {
		if contains(existingCols, field.Name) {
			continue
		}
		oldName, hasOld := oldNames[field.Name] // nil-map read is safe: returns "", false
		if hasOld && contains(existingCols, oldName) {
			if err := d.execDDL(Stmt{Op: OpRenameColumn, Table: tableName, Column: &field, OldName: oldName}, m); err != nil {
				d.logw("sync:", tableName, "rename column", oldName, "to", field.Name, "failed:", err)
			}
		} else {
			if err := d.execDDL(Stmt{Op: OpAddColumn, Table: tableName, Column: &field}, m); err != nil {
				d.logw("sync:", tableName, "add column", field.Name, "failed:", err)
			}
		}
	}

	// 6. Reconcile Safe Drops
	for _, col := range existingCols {
		if schemaHasColumn(schema, col) || isRenameSource(oldNames, col) {
			continue
		}

		// Safe check: SELECT 1 FROM <table> WHERE <col> IS NOT NULL LIMIT 1 — this is a DML
		// read. Compiled with d.conn's Compiler half (storage.Compiler) — the SAME conn used for
		// Exec, not the DDL compiler. ddl.Stmt/ddl.Op cannot express a SELECT with
		// conditions — don't try; reuse storage.Query/storage.Condition as-is for this one case.
		qCheck := storage.Query{
			Action:     storage.ActionReadAll,
			Table:      tableName,
			Columns:    []string{"1"},
			Conditions: []storage.Condition{storage.IsNotNull(col)},
			Limit:      1,
		}
		plan, err := d.conn.Compile(qCheck, m)
		if err != nil {
			d.logw("sync:", tableName, "safe drop check compile failed for column", col, ":", err)
			continue
		}
		rows, err := d.conn.Query(plan.Query, plan.Args...)
		if err != nil {
			d.logw("sync:", tableName, "safe drop check query failed for column", col, ":", err)
			continue
		}
		hasData := rows.Next()
		rows.Close()
		if hasData {
			d.logw("sync:", tableName, "safe drop skip: column", col, "has data")
			continue
		}

		if err := d.execDDL(Stmt{Op: OpDropColumn, Table: tableName, ColumnName: col}, m); err != nil {
			d.logw("sync:", tableName, "drop column", col, "failed:", err)
		}
	}
	return nil
}

func (d *DB) execDDL(s Stmt, m model.Model) error {
	q, args, err := d.ddlCompiler.CompileDDL(s, m)
	if err != nil {
		return err
	}
	return d.conn.Exec(q, args...)
}

// contains, schemaHasColumn, isRenameSource: linear-scan helpers — no map[K]V anywhere in this
// repo (AGENTS.md). These lists are always tiny (one table's columns), a linear scan is free.
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func schemaHasColumn(schema []model.Field, col string) bool {
	for _, f := range schema {
		if f.Name == col {
			return true
		}
	}
	return false
}

// isRenameSource reports whether col is the "from" side of any rename in oldNames. oldNames is
// read-only here (see step 4's comment) — this doesn't allocate a map, it reads one.
func isRenameSource(oldNames map[string]string, col string) bool {
	for _, old := range oldNames {
		if old == col {
			return true
		}
	}
	return false
}
```

> **`registerModel`/`RegisteredModels`.** El `orm.DB` original tenía este housekeeping interno; nunca
> tuvo consumidores fuera de `Sync` y no se traslada aquí tampoco (YAGNI) — si `ddl` necesita en el
> futuro listar modelos sincronizados, se añade cuando haya un consumidor real.

## 3. Diseño del paquete `ddl`

`module github.com/tinywasm/ddl`, `go 1.25.2`. Depende de `tinywasm/storage`, `tinywasm/ddlc`,
`tinywasm/model`, `tinywasm/fmt`. **Cero `tinywasm/orm`.**

### 3.1 `ddl.Compiler`, `Op`, `Stmt` — el contrato DDL del dialecto

```go
package ddl

import (
	"github.com/tinywasm/model"
)

// Op is a DDL operation (schema), distinct from storage's DML Action.
type Op int

const (
	OpCreateTable Op = iota
	OpDropTable
	OpCreateDatabase
	OpAddColumn
	OpRenameColumn
	OpDropColumn
)

// Stmt is one DDL statement to run through a storage.Conn.
type Stmt struct {
	Op       Op
	Table    string
	Database string
	Column   *model.Field // for AddColumn/RenameColumn — full field metadata needed to render the type
	OldName  string       // for RenameColumn
	// ColumnName carries a bare column name for operations that don't need type info —
	// currently only OpDropColumn (you can't "add" or "rename to" a column without knowing
	// its type, but dropping only needs the name). Deliberately separate from Column: giving
	// DropColumn a *model.Field would force callers to fabricate a fake Field just to carry a
	// string, which is worse than a second field that's simply unused by the other Ops.
	ColumnName string
}

// Compiler is implemented by dialect adapters (sqlt, postgres). It renders a DDL Stmt to
// engine SQL. It is the DDL counterpart of storage.Compiler (which stays DML-only).
type Compiler interface {
	CompileDDL(s Stmt, m model.Model) (query string, args []any, err error)
}
```

> `sqlt`/`postgres` tienen estas ramas dentro de su `translate.go` desde el split DDL/DML original.
> Sus planes (`sqlt/docs/PLAN.md`, `postgres/docs/PLAN.md`) las mueven detrás de `CompileDDL`. **No**
> reimplementes la generación SQL aquí: `ddl` orquesta; el dialecto renderiza.

### 3.2 `ddl.DB` — runtime que ejecuta el esquema

```go
package ddl

import (
	"github.com/tinywasm/storage"
	"github.com/tinywasm/model"
)

// DB applies schema changes through a storage.Conn: Exec for the compiled DDL, and Compile (the
// DML half storage.Conn already carries) for Sync's safe-drop SELECT probe. No separate DML
// compiler argument is needed — storage.Conn already unifies Executor+Compiler.
type DB struct {
	conn        storage.Conn
	ddlCompiler Compiler
	log         func(...any)
}

func New(conn storage.Conn, ddlCompiler Compiler) *DB {
	return &DB{conn: conn, ddlCompiler: ddlCompiler}
}

func (d *DB) SetLog(fn func(...any)) { d.log = fn }

func (d *DB) logw(messages ...any) {
	if d.log != nil {
		d.log(messages...)
	}
}
```

- `CreateTable`/`DropTable`/`CreateDatabase`: §2.1.
- `SyncSchema`/`Sync`/`syncAll`/`syncModel`/`execDDL`/helpers: §2.3, íntegro.
- `TableIntrospector`/`SchemaInspector`+`ColumnInfo`: §2.2, íntegro.
- **Receiver `d`, no `storage`.** Este paquete importa `github.com/tinywasm/storage` — si usas `storage` como nombre
  de receiver (`func (storage *DB) ...`), sombreas el paquete dentro de cada método y no puedes escribir
  `storage.Conn`/`storage.Query`/etc. Usa `d` consistentemente, como en §2.1–§2.3.

### 3.3 `ddl/conformance` — contrato ejecutable de DDL (solo backends SQL)

`package conformance`, importa `testing`+`ddl`+`storage`+`model`. Mismo patrón que `storage/conformance` y
`router/conformance`.

```go
type Factory struct {
	Name string
	// New returns a fresh ddl.DB plus the storage.Conn it writes through (for introspection/DML
	// verification) and an introspector to read back the resulting schema. Called once per clause.
	New func(t *testing.T) (schema *ddl.DB, conn storage.Conn, cols func(table string) []string)
}

func Run(t *testing.T, f Factory) {
	t.Run("create_table_makes_expected_columns", ...) // CreateTable(&Widget{}) → cols == [id name qty active]
	t.Run("create_table_is_idempotent", ...)          // segundo CreateTable no falla
	t.Run("sync_adds_new_column", ...)                // Sync con un field extra ⇒ columna nueva presente
	t.Run("drop_table_removes_schema", ...)           // DropTable ⇒ tabla ausente
}
```

Usa el mismo modelo `Widget` que `storage/conformance` (id TEXT PK, name TEXT, qty INT, active BOOL) —
impórtalo de `conformance.Widget` (paquete `github.com/tinywasm/storage/conformance`, ya publicado en
`storage@v0.0.1+`) para no duplicar el fixture.

> Backends que entran: **`sqlt`(sqlite)** y **`postgres`**. `indexdb`/`storage/mem` **no** — no hacen DDL
> SQL.

## 4. Tests del propio repo

- `ddl/conformance` no se auto-prueba (necesita un dialecto real); su cobertura la dan sqlt/postgres.
- `ddl` (runtime) SÍ necesita test propio: un `ddl.Compiler` mock que registre los `Stmt` emitidos, y
  un `storage.Conn` que combine `storage/mock.Executor` (para `Exec`) con `storage/mock.Compiler` (para el
  `dmlCompiler` del safe-drop) — usa `github.com/tinywasm/storage/mock` (ya publicado en `storage@v0.0.1+`), no
  repliques recorders locales. Verifica que `CreateTable`/`Sync` emiten los `Op` correctos en el orden
  correcto (incl. el algoritmo de rename/safe-drop). Cobertura alta del runtime.
- **Sin `map[K]V`** en ningún test ni código de este repo — ver `AGENTS.md`.

## 5. Criterios de aceptación

- `github.com/tinywasm/ddl` existe (gonew, ya hecho), `go 1.25.2`, deps `storage`+`ddlc`+`model`+`fmt`.
  **Cero `tinywasm/orm`.**
- `ddl.New(conn storage.Conn, ddlCompiler ddl.Compiler) *ddl.DB` (2 argumentos) con
  `CreateTable/DropTable/CreateDatabase/Sync/SyncSchema`; `ddl.Compiler`/`ddl.Stmt` (con
  `ColumnName` para `OpDropColumn`, §3.1)/`ddl.Op`; algoritmo `Sync` migrado (con
  `TableIntrospector`/`RenameProvider`), transacción propia vía `boundConn` (sin depender de un
  método `Tx` externo), safe-drop vía `conn.Compile` (el mismo `storage.Conn`, no un segundo argumento).
- `ddl/conformance` con `Run(t, Factory)` + ≥4 cláusulas, reusa `conformance.Widget` de
  `storage/conformance`.
- Test runtime verde contra `storage/mock` (recorders) — sin recorders locales duplicados.
- `ddl` no importa ningún driver SQL. `storage` no depende de `ddl`/`ddlc` (dirección única).
- `gotest` verde; publicado `ddl@v0.0.1` con `gopush`.

## 6. Etapas

| # | Etapa | Archivo(s) | Criterio |
|---|---|---|---|
| 1 | go.mod deps | — | deps `storage`/`ddlc`/`model`/`fmt` añadidas, **sin** `orm` |
| 2 | `Compiler`/`Stmt`/`Op` | `compiler.go` | interfaz DDL del dialecto, `Stmt.ColumnName` incluido (§3.1) |
| 3 | `ddl.DB` + Sync migrado | `db.go`, `sync.go` | `New(conn, ddlCompiler)` 2-arg; `boundConn`; §2.1/§2.3 |
| 4 | introspección | `schema.go` | `TableIntrospector`/`SchemaInspector`/`ColumnInfo` (§2.2) |
| 5 | `ddl/conformance` | `conformance/conformance.go` | `Run`+`Factory`+≥4 cláusulas, reusa `storage/conformance.Widget` |
| 6 | test runtime | `ddl_test.go` | Stmt emitidos correctos vía `storage/mock` |
| 7 | publicar | — | `gotest` verde; `gopush 'feat: runtime DDL + conformance sobre tinywasm/storage'` |

## 7. Cierre

Tras `gopush`, **borra** `docs/PLAN.md`; el diseño duradero pasa a `README.md`/`docs/ARCHITECTURE.md`.
