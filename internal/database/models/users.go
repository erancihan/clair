package models

import "gorm.io/gorm"

type User struct {
	gorm.Model

	ID       uint   `json:"id" gorm:"primaryKey"`
	Username string `json:"username"`
	Email    string `json:"email" gorm:"uniqueIndex"`
	Password string `json:"password"`
}
