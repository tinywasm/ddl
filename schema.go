package ddl

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
