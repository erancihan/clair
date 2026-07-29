package games

// Models returns the GORM models the games domain owns, for the migration set
// composed in internal/database.
//
// This is the convention every domain package follows, alongside Mount:
//
//	func Models() []any
//
// It keeps a domain's tables in the domain's own package. internal/database
// composes the lists rather than naming any model itself, so adding a table is a
// change here and nowhere else - no edit to the AutoMigrate call, and no reason
// for two domains to touch the same line.
//
// Games currently keeps its state in memory, so the list is empty. It stays here
// as the hook the chess domain fills in when game history lands.
func Models() []any {
	return []any{}
}
