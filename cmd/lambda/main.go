package main

import (
	"context"
	"fmt"
	"log"
	"time"

	discordbot "clair/internal/discord-bot"
	"clair/internal/utils"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/getsentry/sentry-go"
)

var (
	SENTRY_DSN         string = ""
	DISCORD_CHANNEL_ID string = ""
	DELAY              *int
)

func init() {
	// display source of log
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// setup Sentry
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              utils.GetEnv("SENTRY_DSN", SENTRY_DSN),
		TracesSampleRate: 0.1,
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

	// TODO: don't fetch from .env
	// > retrieve from .env or self
	DISCORD_CHANNEL_ID := utils.GetEnv("DISCORD_CHANNEL_ID", DISCORD_CHANNEL_ID)
	if DISCORD_CHANNEL_ID == "" {
		// this is a non-recoverable error, FATAL
		sentry.CaptureMessage("DISCORD_CHANNEL_ID is EMPTY")
		log.Fatal("DISCORD_CHANNEL_ID is EMPTY")
	}
}

func handler(ctx context.Context, event events.SQSEvent) (err error) {
	// setup Discord
	discord := discordbot.New()

	for _, message := range event.Records {
		channelId := DISCORD_CHANNEL_ID

		// Process message payload
		response := utils.SQSLambdaToDiscordMessage(message)
		if response == nil {
			// no need to log erroneous message payload
			continue
		}

		// send response message
		_, err = discord.MessageEmbed(channelId, response)
		if err != nil {
			// this _should be_ a recoverable error
			sentry.CaptureException(err)
			log.Printf("%v\n", err)
			log.Printf(">> message : %s\n", fmt.Sprintf("%v", message))
			log.Printf(">> response: %s\n", fmt.Sprintf("%v", response))
			return
		}
	}

	return nil
}

func main() {
	lambda.Start(handler)
}
