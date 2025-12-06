package main

import (
	"io"
	"log"
	"os"
	"telegram-date-bot/handlers"
	"telegram-date-bot/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	err = storage.InitDB("trades_cache.db")
	if err != nil {
		log.Fatal("Error initializing database:", err)
	}

	bot, err := tgbotapi.NewBotAPI(os.Getenv("TELEGRAM_APITOKEN"))
	if err != nil {
		log.Panic(err)
	}
	bot.Debug = false

	// Отключаем логи библиотеки Telegram (переподключения и т.д.)
	tgbotapi.SetLogger(log.New(io.Discard, "", 0))

	log.Printf("Authorized on account %s", bot.Self.UserName)

	go handlers.StartAlertChecker(bot)
	log.Println("Запущен фоновый процесс проверки алертов.")
	go handlers.StartPortfolioNotifier(bot)
	log.Println("Запущен фоновый процесс для PnL-уведомлений.")

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.CallbackQuery != nil {
			handlers.HandleCallback(bot, update)
			continue
		}
		if update.Message == nil {
			continue
		}

		if update.Message.IsCommand() {
			switch update.Message.Command() {
			case "start":
				handlers.HandleStart(bot, update)
			}
			continue
		}

		if update.Message.Text != "" {
			handlers.HandleTextMessageAPI(bot, update)
		}
	}
}
