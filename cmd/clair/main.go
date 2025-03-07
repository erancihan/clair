package main

import (
	"embed"
	"flag"
	"html/template"
	"io"
	"log"
	"os"
	"time"

	awssqshandler "clair/internal/aws-sqs-handler"
	"clair/internal/clair"
	discordbot "clair/internal/discord-bot"
	"clair/internal/server"
	utils "clair/internal/utils"

	sentry "github.com/getsentry/sentry-go"
)

var (
	DISCORD_CHANNEL_ID string = ""
)

//go:generate cp -r ../../templates ./
//go:embed templates/*
var resources embed.FS

var t = template.Must(template.ParseFS(resources, "templates/*"))

func main() {
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

	clair.SetupSentry()

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

	sch := clair.NewScheduler(
		&discord,
		&sqs,
		time.Now().Add(-2*time.Hour).UnixMilli(),
	)
	sch.ScheduleSQS(DISCORD_CHANNEL_ID, time.Duration(*delay)*time.Second)
	defer sch.Close()

	log.Println("Now processing SQS messages")

	s := server.NewServer()
	s.Templates = t
	s.ListenAndServe()

	os.Exit(0)
}
