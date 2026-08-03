package models

import "time"

// BookingSeat is a distinct assigned seat in a seated tier, owning a
// capacity-1 kernel inventory row.
//
// Section, RowName and Number are stored as typed columns rather than being
// parsed back out of Label, which is what makes "two seats together" an ordinary
// query instead of string arithmetic.
//
// Kind separates seats that exist from seats that sell: house, ada, comp and
// kill seats are all real positions in the map that ordinary checkout must not
// hand out.
type BookingSeat struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	TierID    uint      `json:"tier_id" gorm:"index;uniqueIndex:idx_bseat_label"`
	Label     string    `json:"label" gorm:"uniqueIndex:idx_bseat_label;not null"` // "A-12"
	Section   string    `json:"section" gorm:"index"`
	RowName   string    `json:"row_name" gorm:"index"`
	Number    int       `json:"number" gorm:"index"`
	Kind      string    `json:"kind" gorm:"not null;default:sellable"` // sellable|house|ada|comp|kill
}
