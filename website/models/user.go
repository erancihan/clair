package models

import (
	"gorm.io/gorm"
)

type User struct {
	gorm.Model

	Username      string
	Email         string `gorm:"unique;not null"`
	TFASecret     string `gorm:"unique;not null"`
	HasRegistered bool   `gorm:"default:false"`
}
