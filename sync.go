package ddl

import (
	"github.com/tinywasm/model"
	"github.com/tinywasm/storage"
)

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
	var conn storage.Conn = boundConn{TxBoundExecutor: bound, Compiler: d.conn}
	intro, hasIntro := d.conn.(TableIntrospector)
	inspect, hasInspect := d.conn.(SchemaInspector)
	if hasIntro && hasInspect {
		conn = boundConnWithBoth{
			boundConn:         boundConn{TxBoundExecutor: bound, Compiler: d.conn},
			TableIntrospector: intro,
			SchemaInspector:   inspect,
		}
	} else if hasIntro {
		conn = boundConnWithIntrospector{
			boundConn:         boundConn{TxBoundExecutor: bound, Compiler: d.conn},
			TableIntrospector: intro,
		}
	} else if hasInspect {
		conn = boundConnWithSchema{
			boundConn:         boundConn{TxBoundExecutor: bound, Compiler: d.conn},
			SchemaInspector:   inspect,
		}
	}

	txDB := &DB{conn: conn, ddlCompiler: d.ddlCompiler, log: d.log}
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

type boundConnWithIntrospector struct {
	boundConn
	TableIntrospector
}

type boundConnWithSchema struct {
	boundConn
	SchemaInspector
}

type boundConnWithBoth struct {
	boundConn
	TableIntrospector
	SchemaInspector
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

func wrapSyncErr(err error) error {
	return err
}
