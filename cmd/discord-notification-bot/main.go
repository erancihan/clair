package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"time"

	awssqshandler "clair/internal/aws-sqs-handler"
	discordbot "clair/internal/discord-bot"
	timedexecutor "clair/internal/timed-executor"
	utils "clair/internal/utils"

	sentry "github.com/getsentry/sentry-go"
)

var (
	SENTRY_DSN string = ""
	DELAY      *int

	DISCORD_CHANNEL_ID string = ""
)

func init() {
	// display source of log
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// parse flags
	DELAY = flag.Int("delay", 1, "Delay value in seconds")
	flag.Parse()

	// TODO: don't fetch from .env
	// > retrieve from .env or self
	DISCORD_CHANNEL_ID := utils.GetEnv("DISCORD_CHANNEL_ID", DISCORD_CHANNEL_ID)
	if DISCORD_CHANNEL_ID == "" {
		// this is a non-recoverable error, FATAL
		sentry.CaptureMessage("DISCORD_CHANNEL_ID is EMPTY")
		log.Fatal("DISCORD_CHANNEL_ID is EMPTY")
	}
}

func main() {
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

	// setup Discord
	discord := discordbot.New()

	// setup SQS Handler
	sqs := awssqshandler.New()

	// setup Scheduled Executor
	executor := timedexecutor.NewScheduledExecutor(1*time.Minute, time.Duration(*DELAY)*time.Second)
	executor.Start(func() {
		// Every X seconds, process SQS until no messages left
		for {
			message, err := sqs.GetMessage()
			if err != nil {
				sentry.CaptureException(err)
				return
			}
			if message == nil {
				return
			}

			channelId := DISCORD_CHANNEL_ID

			// Process message payload
			response := utils.SQSToDiscordMessage(message)
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
				return
			}

			// Retrieve the message handle of the first message in the queue
			//  (you need the handle to delete the message).
			sqs.DeleteMessage(message.ReceiptHandle)
		}
	})
	defer executor.Close()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop

	log.Println("Graceful shutdown")
}
