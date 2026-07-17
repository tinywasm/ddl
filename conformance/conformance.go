package conformance

import (
	"testing"

	"github.com/tinywasm/ddl"
	"github.com/tinywasm/model"
	"github.com/tinywasm/storage"
	"github.com/tinywasm/storage/conformance"
)

type Factory struct {
	Name string
	// New returns a fresh ddl.DB plus the storage.Conn it writes through (for introspection/DML
	// verification) and an introspector to read back the resulting schema. Called once per clause.
	New func(t *testing.T) (schema *ddl.DB, conn storage.Conn, cols func(table string) []string)
}

func Run(t *testing.T, f Factory) {
	t.Run("create_table_makes_expected_columns", func(t *testing.T) {
		schema, _, cols := f.New(t)
		w := &conformance.Widget{}
		err := schema.CreateTable(w)
		if err != nil {
			t.Fatalf("CreateTable failed: %v", err)
		}
		c := cols(w.ModelName())
		// Expected columns for conformance.Widget are: id, name, qty, active
		expected := []string{"id", "name", "qty", "active"}
		if len(c) != len(expected) {
			t.Fatalf("expected columns %v, got %v", expected, c)
		}
		for _, col := range expected {
			found := false
			for _, x := range c {
				if x == col {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("column %s not found in schema columns %v", col, c)
			}
		}
	})

	t.Run("create_table_is_idempotent", func(t *testing.T) {
		schema, _, _ := f.New(t)
		w := &conformance.Widget{}
		err := schema.CreateTable(w)
		if err != nil {
			t.Fatalf("first CreateTable failed: %v", err)
		}
		err = schema.CreateTable(w)
		if err != nil {
			t.Fatalf("second CreateTable (idempotency) failed: %v", err)
		}
	})

	t.Run("sync_adds_new_column", func(t *testing.T) {
		schema, _, cols := f.New(t)
		w := &conformance.Widget{}
		err := schema.CreateTable(w)
		if err != nil {
			t.Fatalf("CreateTable failed: %v", err)
		}

		// Now we'll sync with an extra column
		fields := append(w.Schema(), model.Field{
			Name: "extra",
			Type: model.Text(),
		})

		err = schema.SyncSchema(w.ModelName(), fields)
		if err != nil {
			t.Fatalf("SyncSchema with extra field failed: %v", err)
		}

		c := cols(w.ModelName())
		found := false
		for _, col := range c {
			if col == "extra" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected column 'extra' to be added by SyncSchema, but columns are %v", c)
		}
	})

	t.Run("drop_table_removes_schema", func(t *testing.T) {
		schema, _, cols := f.New(t)
		w := &conformance.Widget{}
		err := schema.CreateTable(w)
		if err != nil {
			t.Fatalf("CreateTable failed: %v", err)
		}

		err = schema.DropTable(w)
		if err != nil {
			t.Fatalf("DropTable failed: %v", err)
		}

		c := cols(w.ModelName())
		if len(c) > 0 {
			t.Fatalf("expected table to have no columns (or not exist), got columns: %v", c)
		}
	})
}
