package handlers

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"telegram-date-bot/database"
	"telegram-date-bot/exchanges"
	"telegram-date-bot/spotAllPNL"
	"telegram-date-bot/spotpnl"
	"telegram-date-bot/storage"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// состояния нужны, чтобы бот понимал, ключи АПИ или уведлмления ему ожидать.
const (
	StateNone         = ""
	StateWaitingKeys  = "waiting_keys"
	StateWaitingAlert = "waiting_alert"
)

var userStates = make(map[int64]string)

func getUserAndValidateKeys(chatID int64) (database.User, error) {
	user, err := database.GetUser(chatID)
	if err != nil {
		return user, fmt.Errorf("ошибка получения данных")
	}

	if user.BybitApiKey == "" || user.BybitApiSecret == "" {
		return user, fmt.Errorf("ключи не установлены")
	}

	return user, nil
}

// конвертирует кэшированные сделки в формат spotAllPNL
func convertToSpotAllPNLExecutions(cachedTrades []spotpnl.Execution) []spotAllPNL.Execution {
	allTrades := make([]spotAllPNL.Execution, 0, len(cachedTrades))
	for _, t := range cachedTrades {
		allTrades = append(allTrades, spotAllPNL.Execution{
			Symbol:   t.Symbol,
			Price:    t.Price,
			Quantity: t.Quantity,
			Side:     t.Side,
		})
	}
	return allTrades
}

func sendError(bot *tgbotapi.BotAPI, chatID int64, text string) {
	bot.Send(tgbotapi.NewMessage(chatID, "❌ "+text))
}

// редактирует сообщение с новым текстом и клавиатурой
func editMenuMessage(bot *tgbotapi.BotAPI, update tgbotapi.Update, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewEditMessageTextAndMarkup(
		update.CallbackQuery.Message.Chat.ID,
		update.CallbackQuery.Message.MessageID,
		text,
		keyboard,
	)
	bot.Request(msg)
}

// извлекает chatID из update (поддерживает Message и CallbackQuery)
func getChatID(update tgbotapi.Update) int64 {
	if update.Message != nil {
		return update.Message.Chat.ID
	}
	if update.CallbackQuery != nil {
		return update.CallbackQuery.Message.Chat.ID
	}
	return 0
}

func HandleStart(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	chatID := update.Message.Chat.ID
	userName := update.Message.From.FirstName

	welcomeText := fmt.Sprintf(
		"Приветствую, %s! 👋\n"+
			"Я помогу отслеживать твой портфель на Bybit:\n\n"+
			"📊 Мониторинг текущего спот баланса\n"+
			"📈 Расчет PNL за 2 года по спот-торговле\n"+
			"📈 Детальная аналитика по каждой монете\n"+
			"📥 Экспорт PNL в CSV файл\n"+
			"⏰ Оповещения для токенов и портфеля\n\n"+
			"Начни с настройки API ключей в меню ⚙️",
		userName,
	)

	msg := tgbotapi.NewMessage(chatID, welcomeText)
	msg.ReplyMarkup = CreateMainMenuKeyboard()

	bot.Send(msg)
}

func CreateMainMenuKeyboard() tgbotapi.InlineKeyboardMarkup {
	balanceBtn := tgbotapi.NewInlineKeyboardButtonData("📊 Показать баланс", "show_balance")
	totalPnlBtn := tgbotapi.NewInlineKeyboardButtonData("📈 Полный отчет", "show_total_pnl")
	settingsBtn := tgbotapi.NewInlineKeyboardButtonData("⚙️ Настройки", "open_settings")
	alertsBtn := tgbotapi.NewInlineKeyboardButtonData("🔔 Управление алертами", "manage_alerts")

	row1 := tgbotapi.NewInlineKeyboardRow(balanceBtn, totalPnlBtn)
	row2 := tgbotapi.NewInlineKeyboardRow(settingsBtn, alertsBtn)

	return tgbotapi.NewInlineKeyboardMarkup(row1, row2)
}

func CreateSettingsMenuKeyboard(notificationsEnabled bool) tgbotapi.InlineKeyboardMarkup {
	setKeysBtn := tgbotapi.NewInlineKeyboardButtonData("🔑 Настроить ключи API", "set_api_keys")
	exportBtn := tgbotapi.NewInlineKeyboardButtonData("📄 Экспорт в CSV", "export_csv")
	backBtn := tgbotapi.NewInlineKeyboardButtonData("« Назад", "back_to_main")

	var notificationBtn tgbotapi.InlineKeyboardButton
	if notificationsEnabled {
		notificationBtn = tgbotapi.NewInlineKeyboardButtonData("✅ Уведомления (Вкл)", "toggle_notifications_off")
	} else {
		notificationBtn = tgbotapi.NewInlineKeyboardButtonData("❌ Уведомления (Выкл)", "toggle_notifications_on")
	}

	row1 := tgbotapi.NewInlineKeyboardRow(setKeysBtn, exportBtn)
	row2 := tgbotapi.NewInlineKeyboardRow(notificationBtn)
	row3 := tgbotapi.NewInlineKeyboardRow(backBtn)

	return tgbotapi.NewInlineKeyboardMarkup(row1, row2, row3)
}

func createAlertsMenuKeyboard() tgbotapi.InlineKeyboardMarkup {
	createBtn := tgbotapi.NewInlineKeyboardButtonData("➕ Создать новый", "alert_create")
	listBtn := tgbotapi.NewInlineKeyboardButtonData("📄 Мои алерты", "alert_list")
	backBtn := tgbotapi.NewInlineKeyboardButtonData("« Назад", "back_to_main")

	row1 := tgbotapi.NewInlineKeyboardRow(createBtn, listBtn)
	row2 := tgbotapi.NewInlineKeyboardRow(backBtn)

	return tgbotapi.NewInlineKeyboardMarkup(row1, row2)
}

func HandleSetKeys(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	chatID := update.CallbackQuery.Message.Chat.ID

	// Устанавливаем состояние ожидания ключей
	userStates[chatID] = StateWaitingKeys

	msg := tgbotapi.NewMessage(chatID,
		`Отправьте ключи в формате:
API_KEY API_SECRET

Инструкция: [как создать API ключ](https://www.bybit.com/ru-RU/help-center/article/How-to-create-your-API-key/)

🔑 Ваши ключи в безопасности`)
	msg.ParseMode = "MarkdownV2"
	bot.Send(msg)
}

func HandleTextMessageAPI(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	chatID := update.Message.Chat.ID
	text := update.Message.Text

	// Проверяем состояние пользователя
	state := userStates[chatID]

	switch state {
	case StateWaitingKeys:
		// Обрабатываем ввод API ключей
		parts := strings.Fields(text)

		if len(parts) != 2 {
			msg := tgbotapi.NewMessage(chatID, "❌ Неверный формат. Отправьте: API_KEY API_SECRET")
			bot.Send(msg)
			return
		}

		// Сохраняем в users.json (для совместимости)
		user := database.User{
			ChatID:         chatID,
			BybitApiKey:    parts[0],
			BybitApiSecret: parts[1],
		}

		err := database.SaveUser(user)
		if err != nil {
			msg := tgbotapi.NewMessage(chatID, "❌ Ошибка сохранения ключей")
			bot.Send(msg)
			return
		}

		// Сохраняем также в SQLite базу данных
		err = storage.SaveOrUpdateUser(chatID, parts[0], parts[1])
		if err != nil {
			log.Printf("Ошибка сохранения в SQLite: %v", err)
		}

		msg := tgbotapi.NewMessage(chatID, "✅ Ключи сохранены!")
		bot.Send(msg)

		// Сбрасываем состояние
		delete(userStates, chatID)

	case StateWaitingAlert:
		// Обрабатываем создание алерта
		re := regexp.MustCompile(`^(\w+)\s+([\d.]+)$`)
		matches := re.FindStringSubmatch(strings.ToUpper(text))

		if len(matches) == 3 {
			symbolPart := matches[1]
			pricePart := matches[2]

			CreateAlertFromText(bot, update, symbolPart, pricePart)

			// Сбрасываем состояние
			delete(userStates, chatID)
		} else {
			msg := tgbotapi.NewMessage(chatID, "❌ Неверный формат. Используйте: ТИКЕР ЦЕНА\nНапример: BTC 98000")
			bot.Send(msg)
		}

	default:
		// Если состояния нет - игнорируем или показываем подсказку
		msg := tgbotapi.NewMessage(chatID, "Используйте кнопки меню для управления ботом 👇")
		msg.ReplyMarkup = CreateMainMenuKeyboard()
		bot.Send(msg)
	}
}

func HandleCallback(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	callback := tgbotapi.NewCallback(update.CallbackQuery.ID, "")
	bot.Request(callback)

	callbackData := update.CallbackQuery.Data

	if strings.HasPrefix(callbackData, "delete_alert_") {
		HandleDeleteAlert(bot, update)
		return
	}

	switch callbackData {
	case "show_balance":
		HandleBalance(bot, update)
	case "show_total_pnl":
		HandleTotalPNL(bot, update)

	case "open_settings", "back_to_settings":
		chatID := getChatID(update)
		userSettings, _ := storage.GetUserSettings(chatID)
		keyboard := CreateSettingsMenuKeyboard(userSettings.NotificationsEnabled)
		editMenuMessage(bot, update, "⚙️ Настройки:", keyboard)

	case "toggle_notifications_on":
		chatID := getChatID(update)
		storage.SetNotificationsEnabled(chatID, true)
		keyboard := CreateSettingsMenuKeyboard(true)
		editMenuMessage(bot, update, "✅ Уведомления включены!", keyboard)

	case "toggle_notifications_off":
		chatID := getChatID(update)
		storage.SetNotificationsEnabled(chatID, false)
		keyboard := CreateSettingsMenuKeyboard(false)
		editMenuMessage(bot, update, "❌ Уведомления выключены.", keyboard)

	case "manage_alerts":
		ManageAlerts(bot, update)
	case "alert_create":
		chatID := update.CallbackQuery.Message.Chat.ID
		userStates[chatID] = StateWaitingAlert
		newText := "Чтобы создать алерт, отправьте сообщение в формате:\n\n`ТИКЕР ЦЕНА`\n\nНапример: `BTC 98000`"
		msg := tgbotapi.NewMessage(chatID, newText)
		msg.ParseMode = "Markdown"
		bot.Send(msg)
	case "alert_list":
		ShowAlertsList(bot, update)
	case "set_api_keys":
		HandleSetKeys(bot, update)
	case "export_csv":
		HandleExportCSV(bot, update)
	case "back_to_main":
		HandleBackToMainMenu(bot, update)
	case "show_pie_chart":
		HandleBarChart(bot, update)
	}
}

func HandleBalance(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	chatID := getChatID(update)
	sentMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "Обновляю данные портфеля... ⏳"))

	user, err := getUserAndValidateKeys(chatID)
	if err != nil {
		editMsg := tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, "❌ "+err.Error())
		bot.Request(editMsg)
		return
	}

	client := exchanges.NewBybitClient(user.BybitApiKey, user.BybitApiSecret)

	cachedTrades, err := storage.GetAllTradesWithCache(client, chatID)
	if err != nil {
		editMsg := tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, fmt.Sprintf("❌ Ошибка получения истории: %v", err))
		bot.Request(editMsg)
		return
	}

	allTrades := convertToSpotAllPNLExecutions(cachedTrades)

	balances, err := client.GetSpotBalance()
	if err != nil {
		editMsg := tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, fmt.Sprintf("❌ Ошибка получения баланса: %v", err))
		bot.Request(editMsg)
		return
	}

	allPrices, err := client.GetAllMarketPrices()
	if err != nil {
		editMsg := tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, fmt.Sprintf("❌ Ошибка получения цен: %v", err))
		bot.Request(editMsg)
		return
	}

	groupedTrades := spotAllPNL.GroupTradesBySymbol(allTrades)
	tradeAnalysis := spotAllPNL.AnalyzeTradeHistory(groupedTrades)

	var assetsForDisplay []spotpnl.DisplayAsset

	var missingSymbols []string
	for coinName, quantityStr := range balances {
		if coinName == "USDT" || coinName == "USDC" || coinName == "TOTAL" {
			continue
		}

		quantity, _ := strconv.ParseFloat(quantityStr, 64)
		if quantity == 0 {
			continue
		}

		symbol := coinName + "USDT"
		asset := spotpnl.DisplayAsset{
			Name:     coinName,
			Symbol:   symbol,
			Quantity: quantity,
		}

		if analysis, ok := tradeAnalysis[symbol]; ok {
			asset.AvgBuyPrice = analysis.AvgBuyPrice
		}

		if price, ok := allPrices[symbol]; ok {
			asset.CurrentPrice = price
		}

		// Если тикер не найден в общем списке — попробуем получить цену индивидуально и логируем
		if asset.CurrentPrice == 0 {
			log.Printf("[Balance] Тикер не найден в списке: %s — пробую fallback GetCurrentPrice", symbol)
			price, err := spotpnl.GetCurrentPrice(symbol)
			if err != nil {
				log.Printf("[Balance] Fallback не дал цену для %s: %v", symbol, err)
			} else {
				asset.CurrentPrice = price
			}
		}

		if asset.CurrentPrice == 0 {
			missingSymbols = append(missingSymbols, coinName)
		}

		// Всегда считаем текущую стоимость, если известна цена
		if asset.CurrentPrice > 0 {
			asset.CurrentValue = asset.Quantity * asset.CurrentPrice
		}

		// Unrealized PNL и процент считаем только если известна средняя цена покупки
		if asset.AvgBuyPrice > 0 && asset.CurrentPrice > 0 {
			asset.UnrealizedPNL = (asset.CurrentPrice - asset.AvgBuyPrice) * asset.Quantity
			asset.PNLPercentage = (asset.UnrealizedPNL / (asset.AvgBuyPrice * asset.Quantity)) * 100
		}

		assetsForDisplay = append(assetsForDisplay, asset)
	}

	finalMessage := spotpnl.FormatBalancePNLMessage(assetsForDisplay)
	if len(missingSymbols) > 0 {
		finalMessage = finalMessage + "\n\n⚠️ Не найдены цены для: " + strings.Join(missingSymbols, ", ")
	}
	log.Printf("[Balance] User %d: %d активов обработано", chatID, len(assetsForDisplay))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📊 Показать BarChart", "show_pie_chart"),
		),
	)

	editMsg := tgbotapi.NewEditMessageTextAndMarkup(chatID, sentMsg.MessageID, finalMessage, keyboard)
	editMsg.ParseMode = "Markdown"
	bot.Request(editMsg)
}

func HandleTotalPNL(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	chatID := update.CallbackQuery.Message.Chat.ID

	user, err := getUserAndValidateKeys(chatID)
	if err != nil {
		sendError(bot, chatID, err.Error())
		return
	}

	client := exchanges.NewBybitClient(user.BybitApiKey, user.BybitApiSecret)

	cachedTrades, err := storage.GetAllTradesWithCache(client, chatID)
	if err != nil {
		sendError(bot, chatID, fmt.Sprintf("Ошибка расчета общего PNL: %v", err))
		return
	}

	allTrades := convertToSpotAllPNLExecutions(cachedTrades)
	allGroupes := spotAllPNL.GroupTradesBySymbol(allTrades)
	totalPNL := spotAllPNL.AnalyzeTradeHistory(allGroupes)
	formatTotalPNL := spotAllPNL.FormatTotalPNLMessage(totalPNL)

	msg := tgbotapi.NewMessage(chatID, formatTotalPNL)
	msg.ParseMode = "Markdown"
	bot.Send(msg)
}

func HandleSettings(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	chatID := getChatID(update)
	userSettings, _ := storage.GetUserSettings(chatID)
	editMenuMessage(bot, update, "Здесь вы можете управлять настройками:", CreateSettingsMenuKeyboard(userSettings.NotificationsEnabled))
}

func HandleBackToMainMenu(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	editMenuMessage(bot, update, "Главное меню:", CreateMainMenuKeyboard())
}

func ManageAlerts(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	text := "🔔 Управление алертами:\n\nЗдесь вы можете создавать новые алерты или просматривать существующие."
	editMenuMessage(bot, update, text, createAlertsMenuKeyboard())
}

func ShowAlertsList(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	chatID := update.CallbackQuery.Message.Chat.ID

	alerts, err := storage.GetAllActiveAlerts()
	if err != nil {
		errorText := fmt.Sprintf("❌ Ошибка получения алертов: %v", err)
		msg := tgbotapi.NewMessage(chatID, errorText)
		bot.Send(msg)
		return
	}

	// Фильтруем алерты текущего пользователя
	var userAlerts []storage.AlertInfo
	for _, alert := range alerts {
		if alert.UserID == chatID {
			userAlerts = append(userAlerts, alert)
		}
	}

	// Формируем сообщение
	var text string
	var keyboard tgbotapi.InlineKeyboardMarkup

	if len(userAlerts) == 0 {
		text = "📄 У вас пока нет активных алертов.\n\nСоздайте новый через кнопку выше."
		backBtn := tgbotapi.NewInlineKeyboardButtonData("« Назад", "manage_alerts")
		keyboard = tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(backBtn),
		)
	} else {
		text = "📄 *Ваши активные алерты:*\n\n"
		var rows [][]tgbotapi.InlineKeyboardButton

		for _, alert := range userAlerts {
			directionEmoji := "🔼"
			if alert.Direction == "below" {
				directionEmoji = "🔽"
			}

			alertText := fmt.Sprintf("%s `%s` %s `%.2f`",
				directionEmoji, alert.Symbol, alert.Direction, alert.TargetPrice)
			text += alertText + "\n"

			// Кнопка удаления для каждого алерта
			deleteBtn := tgbotapi.NewInlineKeyboardButtonData(
				fmt.Sprintf("🗑 Удалить %s", alert.Symbol),
				fmt.Sprintf("delete_alert_%d", alert.ID),
			)
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(deleteBtn))
		}

		// Кнопка "Назад"
		backBtn := tgbotapi.NewInlineKeyboardButtonData("« Назад", "manage_alerts")
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(backBtn))

		keyboard = tgbotapi.NewInlineKeyboardMarkup(rows...)
	}

	msg := tgbotapi.NewEditMessageTextAndMarkup(
		chatID,
		update.CallbackQuery.Message.MessageID,
		text,
		keyboard,
	)
	msg.ParseMode = "Markdown"
	bot.Request(msg)
}

func HandleDeleteAlert(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	callbackData := update.CallbackQuery.Data
	chatID := update.CallbackQuery.Message.Chat.ID

	parts := strings.Split(callbackData, "_")
	if len(parts) != 3 {
		msg := tgbotapi.NewMessage(chatID, "❌ Ошибка удаления")
		bot.Send(msg)
		return
	}

	alertID, err := strconv.Atoi(parts[2])
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, "❌ Неверный ID алерта")
		bot.Send(msg)
		return
	}

	// Удаляем алерт
	err = storage.DeactivateAlert(alertID)
	if err != nil {
		errorText := fmt.Sprintf("❌ Ошибка удаления: %v", err)
		msg := tgbotapi.NewMessage(chatID, errorText)
		bot.Send(msg)
		return
	}

	// Показываем уведомление
	answerCallback := tgbotapi.NewCallback(update.CallbackQuery.ID, "✅ Алерт удалён")
	bot.Request(answerCallback)

	// Обновляем список алертов
	ShowAlertsList(bot, update)
}

func CreateAlertFromText(bot *tgbotapi.BotAPI, update tgbotapi.Update, symbolPart, pricePart string) {
	chatID := update.Message.Chat.ID

	symbol := strings.ToUpper(symbolPart)
	if !strings.HasSuffix(symbol, "USDT") {
		symbol += "USDT"
	}

	targetPrice, err := strconv.ParseFloat(pricePart, 64)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "Ошибка: неверный формат цены."))
		return
	}
	currentPrice, err := spotpnl.GetCurrentPrice(symbol)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("Не удалось получить цену для %s.", symbol)))
		return
	}

	var direction string
	if targetPrice > currentPrice {
		direction = "up"
	} else {
		direction = "down"
	}
	err = storage.AddAlert(chatID, symbol, targetPrice, direction)
	if err != nil {
		log.Printf("Ошибка сохранения алерта в БД: %v", err)
		bot.Send(tgbotapi.NewMessage(chatID, "Произошла внутренняя ошибка. Попробуйте позже."))
		return
	}

	responseText := fmt.Sprintf("✅ Алерт создан!\n\nМонета: %s\nЦелевая цена: %.2f$", symbol, targetPrice)
	bot.Send(tgbotapi.NewMessage(chatID, responseText))
}

func StartAlertChecker(bot *tgbotapi.BotAPI) {
	ticker := time.NewTicker(180 * time.Second)

	for range ticker.C {
		CheckAndTriggerAlerts(bot)
	}
}

func StartPortfolioNotifier(bot *tgbotapi.BotAPI) {
	ticker := time.NewTicker(23 * time.Hour)

	log.Println("⏰ Запланирован запуск проверки портфеля каждые 23 часа")

	for range ticker.C {
		processAndSendNotifications(bot)
	}
}

func CheckAndTriggerAlerts(bot *tgbotapi.BotAPI) {

	activeAlerts, err := storage.GetAllActiveAlerts()
	if err != nil || len(activeAlerts) == 0 {
		return
	}

	client := exchanges.NewBybitClient("", "")

	// Retry логика для получения цен
	var currentPrices map[string]float64
	maxRetries := 3

	for attempt := 1; attempt <= maxRetries; attempt++ {
		currentPrices, err = client.GetAllMarketPrices()
		if err == nil {
			break // Успешно получили цены
		}

		if attempt < maxRetries {
			log.Printf("⚠️  Попытка %d/%d: Ошибка получения цен для алертов: %v. Повтор через 5 сек...", attempt, maxRetries, err)
			time.Sleep(5 * time.Second)
		} else {
			log.Printf("❌ Не удалось получить цены после %d попыток: %v", maxRetries, err)
			return
		}
	}

	for _, alert := range activeAlerts {

		currentPrice, ok := currentPrices[alert.Symbol]
		if !ok {
			continue
		}

		triggered := false
		if alert.Direction == "up" && currentPrice >= alert.TargetPrice {
			triggered = true
		} else if alert.Direction == "down" && currentPrice <= alert.TargetPrice {
			triggered = true
		}
		if triggered {
			text := fmt.Sprintf(
				"🔔 Сработал алерт! 🔔\n\nМонета: *%s*\nЦена достигла: *%.2f$*",
				alert.Symbol,
				currentPrice,
			)
			msg := tgbotapi.NewMessage(alert.UserID, text)
			msg.ParseMode = "Markdown"
			bot.Send(msg)

			storage.DeactivateAlert(alert.ID)
		}
	}
}

func HandleExportCSV(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	chatID := update.CallbackQuery.Message.Chat.ID
	bot.Send(tgbotapi.NewMessage(chatID, "Готовлю отчет для экспорта... ⏳"))

	user, err := getUserAndValidateKeys(chatID)
	if err != nil {
		sendError(bot, chatID, err.Error())
		return
	}

	client := exchanges.NewBybitClient(user.BybitApiKey, user.BybitApiSecret)

	cachedTrades, err := storage.GetAllTradesWithCache(client, chatID)
	if err != nil {
		sendError(bot, chatID, fmt.Sprintf("Ошибка расчета общего PNL: %v", err))
		return
	}

	allTrades := convertToSpotAllPNLExecutions(cachedTrades)
	allGroupes := spotAllPNL.GroupTradesBySymbol(allTrades)
	totalPNL := spotAllPNL.AnalyzeTradeHistory(allGroupes)

	csvData, err := spotAllPNL.ExportToCSV(totalPNL)
	if err != nil {
		sendError(bot, chatID, fmt.Sprintf("Ошибка при создании CSV: %v", err))
		return
	}

	fileName := fmt.Sprintf("bybit_pnl_report_%s.csv", time.Now().Format("2006-01-02"))
	fileBytes := tgbotapi.FileBytes{
		Name:  fileName,
		Bytes: csvData,
	}
	document := tgbotapi.NewDocument(chatID, fileBytes)
	document.Caption = "Ваш отчет по реализованному PnL готов."
	bot.Send(document)
}

func HandleBarChart(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	chatID := update.CallbackQuery.Message.Chat.ID
	bot.Send(tgbotapi.NewMessage(chatID, "Рисую диаграмму... 🎨"))

	user, err := getUserAndValidateKeys(chatID)
	if err != nil {
		sendError(bot, chatID, err.Error())
		return
	}

	client := exchanges.NewBybitClient(user.BybitApiKey, user.BybitApiSecret)

	balances, err := client.GetSpotBalance()
	if err != nil {
		sendError(bot, chatID, fmt.Sprintf("Ошибка получения баланса: %v", err))
		return
	}

	tickers, err := client.GetMarketTickers("spot")
	if err != nil {
		sendError(bot, chatID, fmt.Sprintf("Ошибка получения цен: %v", err))
		return
	}

	assetValues := make(map[string]float64)

	for coinName, quantityStr := range balances {
		if coinName == "USDT" || coinName == "USDC" || coinName == "TOTAL" {
			continue
		}

		quantity, _ := strconv.ParseFloat(quantityStr, 64)
		if quantity == 0 {
			continue
		}

		symbol := coinName + "USDT"
		var currentPrice float64

		if ticker, ok := tickers[symbol]; ok {
			currentPrice, _ = strconv.ParseFloat(ticker.LastPrice, 64)
		}

		if currentPrice > 0 {
			currentValue := quantity * currentPrice
			assetValues[coinName] = currentValue
		}
	}

	chartImage, err := spotpnl.GeneratePortfolioBarChart(assetValues)
	if err != nil {
		sendError(bot, chatID, fmt.Sprintf("Ошибка создания диаграммы: %v", err))
		return
	}

	photoBytes := tgbotapi.FileBytes{
		Name:  "portfolio_chart.png",
		Bytes: chartImage,
	}

	photoMsg := tgbotapi.NewPhoto(chatID, photoBytes)
	photoMsg.Caption = "Распределение активов в вашем портфеле."
	bot.Send(photoMsg)
}

func processAndSendNotifications(bot *tgbotapi.BotAPI) {
	log.Println("🔍 Запуск проверки для PnL-уведомлений...")

	users, err := storage.GetUsersWithNotificationsEnabled()
	if err != nil {
		log.Printf("❌ Ошибка получения пользователей: %v", err)
		return
	}

	if len(users) == 0 {
		log.Println("⚠️  Нет пользователей с включенными уведомлениями и заполненными API ключами")
		return
	}

	log.Printf("✅ Найдено пользователей для уведомлений: %d", len(users))

	client := exchanges.NewBybitClient("", "")

	// Retry логика для получения цен
	var allPrices map[string]float64
	maxRetries := 3

	for attempt := 1; attempt <= maxRetries; attempt++ {
		allPrices, err = client.GetAllMarketPrices()
		if err == nil {
			break // Успешно получили цены
		}

		if attempt < maxRetries {
			log.Printf("⚠️  Попытка %d/%d: Ошибка получения цен для уведомлений: %v. Повтор через 5 сек...", attempt, maxRetries, err)
			time.Sleep(5 * time.Second)
		} else {
			log.Printf("❌ Не удалось получить цены после %d попыток: %v", maxRetries, err)
			return
		}
	}

	for _, user := range users {
		log.Printf("📊 Обработка пользователя %d...", user.UserID)

		client := exchanges.NewBybitClient(user.ApiKey, user.ApiSecret)

		balances, err := client.GetSpotBalance()
		if err != nil {
			log.Printf("❌ Ошибка получения баланса для user %d: %v", user.UserID, err)
			continue
		}

		currentValue := calculateTotalValue(balances, allPrices)
		log.Printf("💰 Текущая стоимость портфеля user %d: %.2f$", user.UserID, currentValue)

		storage.SavePortfolioSnapshot(user.UserID, currentValue)
		log.Printf("💾 Снимок портфеля сохранен для user %d", user.UserID)

		twentyThreeHoursAgo := time.Now().Add(-23 * time.Hour).Unix()
		previousValue, err := storage.GetLatestSnapshotBefore(user.UserID, twentyThreeHoursAgo)

		if err == nil && previousValue > 0 {
			diffValue := currentValue - previousValue
			diffPercent := (diffValue / previousValue) * 100
			log.Printf("📈 Изменение для user %d: %.2f$ (%.2f%%)", user.UserID, diffValue, diffPercent)
			sendNotification(bot, user.UserID, currentValue, diffValue, diffPercent)
		} else {
			log.Printf("ℹ️  Для user %d нет предыдущего снимка или ошибка: %v", user.UserID, err)
		}
	}
	log.Println("✅ Проверка для PnL-уведомлений завершена.")
}

func calculateTotalValue(balances map[string]string, prices map[string]float64) float64 {
	var totalValue float64
	for coin, qtyStr := range balances {
		qty, _ := strconv.ParseFloat(qtyStr, 64)
		price, ok := prices[coin+"USDT"]
		if ok {
			totalValue += qty * price
		}
	}
	return totalValue
}

func sendNotification(bot *tgbotapi.BotAPI, userID int64, currentValue, diffValue, diffPercent float64) {
	sign := "+"
	emoji := "📈"
	if diffValue < 0 {
		sign = "" // Минус будет у самого числа
		emoji = "📉"
	}

	text := fmt.Sprintf(
		"%s *Ежедневная сводка по портфелю*\n\n"+
			"За последние 24 часа ваш портфель изменился на *%s%.2f$ (%.2f%%)*.\n\n"+
			"Текущая стоимость: *%.2f$*",
		emoji, sign, diffValue, diffPercent, currentValue,
	)

	msg := tgbotapi.NewMessage(userID, text)
	msg.ParseMode = "Markdown"
	bot.Send(msg)
}
