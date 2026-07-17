package ddl

import (
	"github.com/tinywasm/model"
	"github.com/tinywasm/storage"
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
