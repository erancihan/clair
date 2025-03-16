package models

import "gorm.io/gorm"

type User struct {
	gorm.Model

	ID       int    `json:"id" gorm:"primaryKey"`
	Username string `json:"username"`
	Password string `json:"password"`
}
