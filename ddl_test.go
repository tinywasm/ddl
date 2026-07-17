package ddl_test

import (
	"errors"
	"testing"

	"github.com/tinywasm/ddl"
	"github.com/tinywasm/model"
	"github.com/tinywasm/storage"
	"github.com/tinywasm/storage/mock"
)

type mockDDLCompiler struct {
	Stmts []ddl.Stmt
	Err   error
}

func (m *mockDDLCompiler) CompileDDL(s ddl.Stmt, model model.Model) (string, []any, error) {
	m.Stmts = append(m.Stmts, s)
	if m.Err != nil {
		return "", nil, m.Err
	}
	return s.Table + "_compiled", nil, nil
}

type mockConn struct {
	*mock.Executor
	*mock.Compiler
	columns []string
	colsErr error
}

func (m *mockConn) TableColumns(table string) ([]string, error) {
	return m.columns, m.colsErr
}

func TestCreateTable(t *testing.T) {
	mExec := &mock.Executor{}
	mComp := &mock.Compiler{}
	conn := &mockConn{Executor: mExec, Compiler: mComp}
	ddlComp := &mockDDLCompiler{}

	db := ddl.New(conn, ddlComp)
	m := &mock.Model{Table: "test_table"}

	err := db.CreateTable(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ddlComp.Stmts) != 1 || ddlComp.Stmts[0].Op != ddl.OpCreateTable || ddlComp.Stmts[0].Table != "test_table" {
		t.Fatalf("unexpected DDL compiled statement: %v", ddlComp.Stmts)
	}

	if len(mExec.ExecutedQueries) != 1 || mExec.ExecutedQueries[0] != "test_table_compiled" {
		t.Fatalf("unexpected executed query: %v", mExec.ExecutedQueries)
	}
}

func TestDropTable(t *testing.T) {
	mExec := &mock.Executor{}
	mComp := &mock.Compiler{}
	conn := &mockConn{Executor: mExec, Compiler: mComp}
	ddlComp := &mockDDLCompiler{}

	db := ddl.New(conn, ddlComp)
	m := &mock.Model{Table: "test_table"}

	err := db.DropTable(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ddlComp.Stmts) != 1 || ddlComp.Stmts[0].Op != ddl.OpDropTable || ddlComp.Stmts[0].Table != "test_table" {
		t.Fatalf("unexpected DDL compiled statement: %v", ddlComp.Stmts)
	}
}

func TestCreateDatabase(t *testing.T) {
	mExec := &mock.Executor{}
	mComp := &mock.Compiler{}
	conn := &mockConn{Executor: mExec, Compiler: mComp}
	ddlComp := &mockDDLCompiler{}

	db := ddl.New(conn, ddlComp)

	err := db.CreateDatabase("test_db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ddlComp.Stmts) != 1 || ddlComp.Stmts[0].Op != ddl.OpCreateDatabase || ddlComp.Stmts[0].Database != "test_db" {
		t.Fatalf("unexpected DDL compiled statement: %v", ddlComp.Stmts)
	}
}

func TestSync_NoIntrospector(t *testing.T) {
	// A connection that does not implement TableIntrospector
	mExec := &mock.Executor{}
	mComp := &mock.Compiler{}
	conn := struct {
		storage.Executor
		storage.Compiler
	}{
		Executor: mExec,
		Compiler: mComp,
	}
	ddlComp := &mockDDLCompiler{}

	db := ddl.New(conn, ddlComp)
	m := &mock.Model{
		Table: "test_table",
		Sch: []model.Field{
			{Name: "id", Type: model.Text()},
			{Name: "name", Type: model.Text()},
		},
	}

	err := db.Sync(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have CreateTable, then fallback to additive (AddColumn id, AddColumn name)
	if len(ddlComp.Stmts) != 3 {
		t.Fatalf("expected 3 statements, got %d", len(ddlComp.Stmts))
	}
	if ddlComp.Stmts[0].Op != ddl.OpCreateTable {
		t.Errorf("expected OpCreateTable, got %v", ddlComp.Stmts[0].Op)
	}
	if ddlComp.Stmts[1].Op != ddl.OpAddColumn || ddlComp.Stmts[1].Column.Name != "id" {
		t.Errorf("expected OpAddColumn for id, got %v", ddlComp.Stmts[1])
	}
	if ddlComp.Stmts[2].Op != ddl.OpAddColumn || ddlComp.Stmts[2].Column.Name != "name" {
		t.Errorf("expected OpAddColumn for name, got %v", ddlComp.Stmts[2])
	}
}

func TestSync_WithIntrospector_NoChanges(t *testing.T) {
	mExec := &mock.Executor{}
	mComp := &mock.Compiler{}
	conn := &mockConn{
		Executor: mExec,
		Compiler: mComp,
		columns:  []string{"id", "name"},
	}
	ddlComp := &mockDDLCompiler{}

	db := ddl.New(conn, ddlComp)
	m := &mock.Model{
		Table: "test_table",
		Sch: []model.Field{
			{Name: "id", Type: model.Text()},
			{Name: "name", Type: model.Text()},
		},
	}

	err := db.Sync(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only CreateTable is called since columns match
	if len(ddlComp.Stmts) != 1 || ddlComp.Stmts[0].Op != ddl.OpCreateTable {
		t.Fatalf("expected only CreateTable statement, got %v", ddlComp.Stmts)
	}
}

func TestSync_WithIntrospector_AddColumn(t *testing.T) {
	mExec := &mock.Executor{}
	mComp := &mock.Compiler{}
	conn := &mockConn{
		Executor: mExec,
		Compiler: mComp,
		columns:  []string{"id"},
	}
	ddlComp := &mockDDLCompiler{}

	db := ddl.New(conn, ddlComp)
	m := &mock.Model{
		Table: "test_table",
		Sch: []model.Field{
			{Name: "id", Type: model.Text()},
			{Name: "name", Type: model.Text()},
		},
	}

	err := db.Sync(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have CreateTable, and AddColumn name
	if len(ddlComp.Stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(ddlComp.Stmts))
	}
	if ddlComp.Stmts[0].Op != ddl.OpCreateTable {
		t.Errorf("expected OpCreateTable, got %v", ddlComp.Stmts[0].Op)
	}
	if ddlComp.Stmts[1].Op != ddl.OpAddColumn || ddlComp.Stmts[1].Column.Name != "name" {
		t.Errorf("expected OpAddColumn for name, got %v", ddlComp.Stmts[1])
	}
}

type renameModel struct {
	*mock.Model
	oldNames map[string]string
}

func (rm *renameModel) OldNames() map[string]string {
	return rm.oldNames
}

func TestSync_WithIntrospector_Rename(t *testing.T) {
	mExec := &mock.Executor{}
	mComp := &mock.Compiler{}
	conn := &mockConn{
		Executor: mExec,
		Compiler: mComp,
		columns:  []string{"id", "old_name"},
	}
	ddlComp := &mockDDLCompiler{}

	db := ddl.New(conn, ddlComp)
	base := &mock.Model{
		Table: "test_table",
		Sch: []model.Field{
			{Name: "id", Type: model.Text()},
			{Name: "new_name", Type: model.Text()},
		},
	}
	m := &renameModel{
		Model:    base,
		oldNames: map[string]string{"new_name": "old_name"},
	}

	err := db.Sync(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should compile CreateTable, and RenameColumn old_name -> new_name
	if len(ddlComp.Stmts) != 2 {
		t.Fatalf("expected 2 statements, got %v", ddlComp.Stmts)
	}
	if ddlComp.Stmts[0].Op != ddl.OpCreateTable {
		t.Errorf("expected OpCreateTable, got %v", ddlComp.Stmts[0].Op)
	}
	stmt := ddlComp.Stmts[1]
	if stmt.Op != ddl.OpRenameColumn || stmt.Column.Name != "new_name" || stmt.OldName != "old_name" {
		t.Errorf("expected OpRenameColumn from old_name to new_name, got %v", stmt)
	}
}

func TestSync_WithIntrospector_SafeDrop_WithData(t *testing.T) {
	mExec := &mock.Executor{
		ReturnQueryRows: &mock.Rows{
			Count: 1, // Has data!
		},
	}
	mComp := &mock.Compiler{
		ReturnPlan: storage.Plan{Query: "SELECT 1"},
	}
	conn := &mockConn{
		Executor: mExec,
		Compiler: mComp,
		columns:  []string{"id", "obsolete_column"},
	}
	ddlComp := &mockDDLCompiler{}

	db := ddl.New(conn, ddlComp)
	m := &mock.Model{
		Table: "test_table",
		Sch: []model.Field{
			{Name: "id", Type: model.Text()},
		},
	}

	err := db.Sync(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have CreateTable but NOT DropColumn obsolete_column because it has data
	if len(ddlComp.Stmts) != 1 || ddlComp.Stmts[0].Op != ddl.OpCreateTable {
		t.Fatalf("expected only CreateTable statement, got %v", ddlComp.Stmts)
	}
}

func TestSync_WithIntrospector_SafeDrop_NoData(t *testing.T) {
	mExec := &mock.Executor{
		ReturnQueryRows: &mock.Rows{
			Count: 0, // No data! Safe to drop.
		},
	}
	mComp := &mock.Compiler{
		ReturnPlan: storage.Plan{Query: "SELECT 1"},
	}
	conn := &mockConn{
		Executor: mExec,
		Compiler: mComp,
		columns:  []string{"id", "obsolete_column"},
	}
	ddlComp := &mockDDLCompiler{}

	db := ddl.New(conn, ddlComp)
	m := &mock.Model{
		Table: "test_table",
		Sch: []model.Field{
			{Name: "id", Type: model.Text()},
		},
	}

	err := db.Sync(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have CreateTable and DropColumn obsolete_column
	if len(ddlComp.Stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(ddlComp.Stmts))
	}
	if ddlComp.Stmts[0].Op != ddl.OpCreateTable {
		t.Errorf("expected OpCreateTable, got %v", ddlComp.Stmts[0].Op)
	}
	if ddlComp.Stmts[1].Op != ddl.OpDropColumn || ddlComp.Stmts[1].ColumnName != "obsolete_column" {
		t.Errorf("expected OpDropColumn for obsolete_column, got %v", ddlComp.Stmts[1])
	}
}

// mockTxConn combines mockConn and storage.TxExecutor to test transaction wrapping.
type mockTxConn struct {
	*mockConn
	BeginTxCalled bool
	TxBound       *mock.TxBoundExecutor
}

func (m *mockTxConn) BeginTx() (storage.TxBoundExecutor, error) {
	m.BeginTxCalled = true
	return m.TxBound, nil
}

func TestSync_Transaction(t *testing.T) {
	mExec := &mock.Executor{}
	mComp := &mock.Compiler{}
	mConn := &mockConn{
		Executor: mExec,
		Compiler: mComp,
	}
	txBound := &mock.TxBoundExecutor{}
	txConn := &mockTxConn{
		mockConn: mConn,
		TxBound:  txBound,
	}
	ddlComp := &mockDDLCompiler{}

	db := ddl.New(txConn, ddlComp)
	m := &mock.Model{Table: "test_table"}

	err := db.Sync(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !txConn.BeginTxCalled {
		t.Errorf("expected BeginTx to be called")
	}
	if !txBound.CommitCalled {
		t.Errorf("expected Commit to be called on bound executor")
	}
}

func TestSync_Transaction_WithIntrospector(t *testing.T) {
	mExec := &mock.Executor{}
	mComp := &mock.Compiler{}
	mConn := &mockConn{
		Executor: mExec,
		Compiler: mComp,
		columns:  []string{"id"},
	}
	txBound := &mock.TxBoundExecutor{}
	txConn := &mockTxConn{
		mockConn: mConn,
		TxBound:  txBound,
	}
	ddlComp := &mockDDLCompiler{}

	db := ddl.New(txConn, ddlComp)
	m := &mock.Model{
		Table: "test_table",
		Sch: []model.Field{
			{Name: "id", Type: model.Text()},
			{Name: "name", Type: model.Text()},
		},
	}

	err := db.Sync(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !txConn.BeginTxCalled {
		t.Errorf("expected BeginTx to be called")
	}
	if !txBound.CommitCalled {
		t.Errorf("expected Commit to be called on bound executor")
	}

	// Introspection forwarding is active, so we only add 'name'
	if len(ddlComp.Stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(ddlComp.Stmts))
	}
	if ddlComp.Stmts[0].Op != ddl.OpCreateTable {
		t.Errorf("expected OpCreateTable, got %v", ddlComp.Stmts[0].Op)
	}
	if ddlComp.Stmts[1].Op != ddl.OpAddColumn || ddlComp.Stmts[1].Column.Name != "name" {
		t.Errorf("expected OpAddColumn for name, got %v", ddlComp.Stmts[1])
	}
}

func TestSync_Transaction_Rollback(t *testing.T) {
	mExec := &mock.Executor{}
	mComp := &mock.Compiler{}
	mConn := &mockConn{
		Executor: mExec,
		Compiler: mComp,
	}
	txBound := &mock.TxBoundExecutor{}
	txConn := &mockTxConn{
		mockConn: mConn,
		TxBound:  txBound,
	}
	// Let's force an error in the compilation to trigger rollback
	ddlComp := &mockDDLCompiler{Err: errors.New("compile error")}

	db := ddl.New(txConn, ddlComp)
	m := &mock.Model{Table: "test_table"}

	err := db.Sync(m)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	if !txConn.BeginTxCalled {
		t.Errorf("expected BeginTx to be called")
	}
	if txBound.CommitCalled {
		t.Errorf("expected Commit NOT to be called")
	}
	if !txBound.RollbackCalled {
		t.Errorf("expected Rollback to be called on error")
	}
}
