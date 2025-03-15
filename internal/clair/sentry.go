package clair

import (
	"log"
	"time"

	utils "github.com/erancihan/clair/internal/utils"
	"github.com/getsentry/sentry-go"
)

func SetupSentry() {
	SENTRY_DSN := utils.GetEnv("SENTRY_DSN", "")
	ENVIRONMENT := utils.GetEnv("ENVIRONMENT", "development")

	// setup Sentry
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              SENTRY_DSN,
		TracesSampleRate: 0.1,
		BeforeSend: func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
			if ENVIRONMENT == "development" {
				return nil
			}

			return event
		},
	})
	if err != nil {
		log.Fatalf("Failed sentry.Init: %s\n", err)
	}
	// Flush buffered events before the program terminates.
	defer func() {
		err := recover()

		if err != nil {
			sentry.CurrentHub().Recover(err)
			sentry.Flush(5 * time.Second)
		}
	}()
}
