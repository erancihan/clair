package discordbot

import (
	"github.com/bwmarrin/discordgo"
	"github.com/erancihan/clair/internal/utils"
	"go.uber.org/zap"
)

var (
	DISCORD_BOT_AUTH_KEY        string = ""
	DISCORD_BOT_IDENTIFIER      string = ""
	DISCORD_BOT_SELF_IDENTIFIER string = "<@!" + DISCORD_BOT_IDENTIFIER + ">"
)

type DiscordBot struct {
	session *discordgo.Session
	logger  *zap.Logger
}

func New() DiscordBot {
	logger := utils.NewLogger("discord-bot")
	defer func() { _ = logger.Sync() }()

	authKey := utils.GetEnv("DISCORD_BOT_AUTH_KEY", DISCORD_BOT_AUTH_KEY)
	if authKey == "" {
		logger.Fatal("BOT Auth Key cannot be empty")
	}

	sess, err := discordgo.New("Bot " + authKey)
	if err != nil {
		logger.Sugar().Fatalf("Invalid bot parameters: %v", err)
	}
	sess.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		logger.Info("Bot is up!")
	})

	bot := DiscordBot{
		session: sess,
		logger:  logger,
	}

	return bot
}

func (bot *DiscordBot) Connect() error {
	bot.logger.Info("Discord Bot Connecting...")
	return bot.session.Open()
}

func (bot *DiscordBot) Disconnect() error {
	bot.logger.Info("Discord Bot Disconnecting...")
	return bot.session.Close()
}

func (bot *DiscordBot) MessageEmbed(channelId string, data *discordgo.MessageSend) (st *discordgo.Message, err error) {
	return bot.session.ChannelMessageSendComplex(channelId, data)
}

func (bot *DiscordBot) MessageText(channelId, message string) (st *discordgo.Message, err error) {
	return bot.session.ChannelMessageSend(channelId, message)
}
