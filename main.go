package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/m1guelpf/chatgpt-telegram/src/chatgpt"
	"github.com/m1guelpf/chatgpt-telegram/src/config"
	"github.com/m1guelpf/chatgpt-telegram/src/session"
	"github.com/m1guelpf/chatgpt-telegram/src/tgbot"
)

const (
	messageStart = "Send a message to start talking with ChatGPT. You can use /reload at any point to clear the conversation history and start from scratch (don't worry, it won't delete the Telegram messages)."
	messageHelp  = `/reload - clear chatGPT conversation history (Telegram messages will not be deleted)
/setToken <token> - set the openAI session token`
)

func main() {
	envConfig, err := config.LoadEnvConfig(".env")
	if err != nil {
		log.Fatalf("Couldn't load .env config: %v", err)
	}
	if err := envConfig.ValidateWithDefaults(); err != nil {
		log.Fatalf("Invalid .env config: %v", err)
	}

	persistentConfig, err := config.LoadOrCreatePersistentConfig()
	if err != nil {
		log.Fatalf("Couldn't load config: %v", err)
	}

	if persistentConfig.OpenAISession == "" && !envConfig.ManualAuth {
		token, err := session.GetSession()
		if err != nil {
			log.Fatalf("Couldn't get OpenAI session: %v", err)
		}

		if err = persistentConfig.SetSessionToken(token); err != nil {
			log.Fatalf("Couldn't save OpenAI session: %v", err)
		}
	}

	chatGPT := chatgpt.Init(persistentConfig)
	log.Println("Started ChatGPT")

	bot, err := tgbot.New(envConfig.TelegramToken, time.Duration(envConfig.EditWaitSeconds))
	if err != nil {
		log.Fatalf("Couldn't start Telegram bot: %v", err)
	}

	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		bot.Stop()
		os.Exit(0)
	}()

	log.Printf("Started Telegram bot! Message @%s to start.", bot.Username)

	for update := range bot.GetUpdatesChan() {
		if update.Message == nil {
			continue
		}

		var (
			updateText       = update.Message.Text
			updateChatID     = update.Message.Chat.ID
			updateMessageID  = update.Message.MessageID
			updateUserID     = update.Message.From.ID
			isChatBotInGroup = update.Message.Chat.IsGroup() || update.Message.Chat.IsSuperGroup()
			isAtChatBot      = strings.HasPrefix(update.Message.Text, "@"+bot.Username) || strings.HasSuffix(update.Message.Text, "@"+bot.Username)
			isPrivateChat    = update.Message.Chat.IsPrivate()
		)

		if len(envConfig.TelegramID) != 0 && !envConfig.HasTelegramID(updateUserID) {
			log.Printf("User %d is not allowed to use this bot", updateUserID)
			bot.Send(updateChatID, updateMessageID, "You are not authorized to use this bot.")
			continue
		}

		if !update.Message.IsCommand() && (isPrivateChat || (isChatBotInGroup && isAtChatBot)) {
			bot.SendTyping(updateChatID)

			feed, err := chatGPT.SendMessage(updateText, updateChatID)
			if err != nil {
				bot.Send(updateChatID, updateMessageID, fmt.Sprintf("Error: %v", err))
			} else {
				bot.SendAsLiveOutput(updateChatID, updateMessageID, feed)
			}
			continue
		}

		var text string
		switch update.Message.Command() {
		case "help":
			text = messageHelp
		case "start":
			text = messageStart
		case "setToken":
			token := update.Message.CommandArguments()
			if token == "" {
				text = "Please provide a token. Example: /setToken eyJhB..."
				break
			}
			if err := persistentConfig.SetSessionToken(token); err != nil {
				text = fmt.Sprintf("Error: %v", err)
				break
			}
			text = "Token set successfully."
		case "reload":
			chatGPT.ResetConversation(updateChatID)
			text = "Started a new conversation. Enjoy!"
		default:
			text = "Unknown command. Send /help to see a list of commands."
		}

		if _, err := bot.Send(updateChatID, updateMessageID, text); err != nil {
			log.Printf("Error sending message: %v", err)
		}
	}
}
