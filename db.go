package ddl

import (
	"github.com/tinywasm/model"
)

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

func (d *DB) SetLog(fn func(...any)) { d.log = fn }

func (d *DB) logw(messages ...any) {
	if d.log != nil {
		d.log(messages...)
	}
}

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
