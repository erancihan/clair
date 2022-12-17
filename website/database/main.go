package database

import (
	"clair/website/models"
	"log"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var conn *gorm.DB

func New() (*gorm.DB, error) {
	var err error

	conn, err = gorm.Open(sqlite.Open(".opt/dev.db"), &gorm.Config{})
	if err != nil {
		log.Fatal(err)
	}

	if conn == nil {
		log.Fatalln("DB cannot be empty")
	}

	conn.AutoMigrate(&models.User{})

	return conn, err
}

func Conn() *gorm.DB {
	if conn == nil {
		log.Fatalln("DB cannot be empty")
	}

	return conn
}
