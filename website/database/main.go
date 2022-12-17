package database

import (
	"clair/website/models"
	"log"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func New() {
	db, err := gorm.Open(sqlite.Open(".opt/dev.db"), &gorm.Config{})
	if err != nil {
		log.Fatal(err)
	}

	if db == nil {
		log.Fatalln("DB cannot be empty")
	}

	db.AutoMigrate(&models.User{})
}
