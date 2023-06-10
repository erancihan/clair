package main

import (
	"embed"
	"flag"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	awssqshandler "clair/internal/aws-sqs-handler"
	discordbot "clair/internal/discord-bot"
	"clair/internal/scheduler"
	utils "clair/internal/utils"

	sentry "github.com/getsentry/sentry-go"
)

var (
	SENTRY_DSN string = ""

	DISCORD_CHANNEL_ID string = ""
)

//go:generate cp -r ../../templates ./
//go:embed templates/*
var resources embed.FS

var t = template.Must(template.ParseFS(resources, "templates/*"))

func main() {
	var err error

	// parse flags
	delay := flag.Int("delay", 1, "Delay value in seconds")
	isVerbose := flag.Bool("verbose", false, "Verbose output")
	doLog := flag.Bool("log", false, "Log output")
	flag.Parse()

	// display source of log
	log.SetFlags(log.LstdFlags | log.Lshortfile | log.Lmicroseconds)

	// get log output file
	if *doLog {
		f, err := os.OpenFile("./clair.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			log.Fatalf("error opening file: %v", err)
		}
		defer f.Close()

		// setup log
		if *isVerbose {
			// if verbose, log to both file and stdout
			writer := io.MultiWriter(f, os.Stdout)
			log.SetOutput(writer)
		} else {
			// otherwise just file
			log.SetOutput(f)
		}
	}

	// setup Sentry
	err = sentry.Init(sentry.ClientOptions{
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

	log.Printf("Setting up with delay: %v\n", *delay)

	DISCORD_CHANNEL_ID := utils.GetEnv("DISCORD_CHANNEL_ID", DISCORD_CHANNEL_ID)
	if DISCORD_CHANNEL_ID == "" {
		// this is a non-recoverable error, FATAL
		sentry.CaptureMessage("DISCORD_CHANNEL_ID is EMPTY")
		log.Fatal("DISCORD_CHANNEL_ID is EMPTY")
	}

	// setup Discord
	discord := discordbot.New()

	// setup SQS Handler
	sqs := awssqshandler.New()

	sch := scheduler.New(
		&discord,
		&sqs,
		time.Now().Add(-2*time.Hour).UnixMilli(),
	)
	sch.ScheduleSQS(DISCORD_CHANNEL_ID, time.Duration(*delay)*time.Second)
	defer sch.Close()

	log.Println("Now processing SQS messages")

	// start the server
	port := utils.GetEnv("PORT", "8080")

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]string{
			"Region": os.Getenv("FLY_REGION"),
		}

		t.ExecuteTemplate(w, "index.html.tmpl", data)
	})

	log.Println("listening on", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
