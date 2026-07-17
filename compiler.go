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
