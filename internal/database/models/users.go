package models

import "gorm.io/gorm"

type User struct {
	gorm.Model

	ID       uint   `json:"id" gorm:"primaryKey"`
	Username string `json:"username"`
	Email    string `json:"email" gorm:"uniqueIndex"`
	// Password holds the bcrypt hash and is never serialized: this struct is
	// encoded straight to JSON by user-facing handlers, so a marshalable hash
	// leaks credentials. Requests carry passwords in their own payload types.
	Password string `json:"-"`
	// Role is the coarse authorization level for the user. It defaults to the
	// generic "user" role at the database level so existing rows and new
	// registrations are always assigned a role.
	Role string `json:"role" gorm:"index;default:user"`
}
