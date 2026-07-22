package models

import "gorm.io/gorm"

type User struct {
	gorm.Model

	ID       uint   `json:"id" gorm:"primaryKey"`
	Username string `json:"username"`
	Email    string `json:"email" gorm:"uniqueIndex"`
	Password string `json:"password"`
	// Role is the coarse authorization level for the user. It defaults to the
	// generic "user" role at the database level so existing rows and new
	// registrations are always assigned a role.
	Role string `json:"role" gorm:"index;default:user"`
}
