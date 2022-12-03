package migrations

import (
	"log"

	"mercury/website/models"

	"gorm.io/gorm"
)

func Run(db *gorm.DB) {
	if db == nil {
		log.Fatalln("DB cannot be fatal")
	}

	db.AutoMigrate(&models.User{})
}
