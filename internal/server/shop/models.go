package shop

// Models returns the GORM models the shop domain owns, for the migration set
// composed in internal/database.
//
// Same convention as the games and booking domains: the domain keeps its own
// table list, internal/database appends it, and putting a table under
// migration is a change here rather than an edit to the AutoMigrate call.
//
// Empty until the catalog lands (docs/shop-plan.md §2, Phase 1).
func Models() []any {
	return []any{}
}
