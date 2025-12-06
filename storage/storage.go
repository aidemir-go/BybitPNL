package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"telegram-date-bot/exchanges"
	"telegram-date-bot/spotpnl"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var DB *sql.DB

type AlertInfo struct {
	ID          int
	UserID      int64
	Symbol      string
	TargetPrice float64
	Direction   string
}

type UserSettings struct {
	UserID               int64
	NotificationsEnabled bool
	// Можно добавить другие настройки в будущем
}

type User struct {
	UserID    int64
	ApiKey    string
	ApiSecret string
}

func InitDB(filepath string) error {
	var err error
	DB, err = sql.Open("sqlite3", filepath)
	if err != nil {
		return err
	}

	createTradeHistorySQL := `CREATE TABLE IF NOT EXISTS trade_history (
		user_id INTEGER PRIMARY KEY,
		trades TEXT,
		last_update INTEGER
	);`
	if _, err := DB.Exec(createTradeHistorySQL); err != nil {
		return err
	}

	createAlertsTableSQL := `CREATE TABLE IF NOT EXISTS alerts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER,
		symbol TEXT,
		target_price REAL,
		direction TEXT,
		is_active INTEGER DEFAULT 1
	);`
	if _, err := DB.Exec(createAlertsTableSQL); err != nil {
		return err
	}

	createUsersTableSQL := `CREATE TABLE IF NOT EXISTS users (
		user_id INTEGER PRIMARY KEY,
		bybit_api_key TEXT,
		bybit_api_secret TEXT
	);`
	if _, err := DB.Exec(createUsersTableSQL); err != nil {
		return err
	}

	alterUsersTableSQL := `
		PRAGMA foreign_keys=off;
		ALTER TABLE users ADD COLUMN notifications_enabled INTEGER DEFAULT 0;
		PRAGMA foreign_keys=on;
	`
	DB.Exec(alterUsersTableSQL)

	createSnapshotsTableSQL := `CREATE TABLE IF NOT EXISTS portfolio_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER,
		portfolio_value REAL,
		timestamp INTEGER
	);`
	if _, err := DB.Exec(createSnapshotsTableSQL); err != nil {
		return err
	}

	log.Println("База данных успешно инициализирована/обновлена.")
	return nil
}

func SaveTradesToCache(userID int64, trades []spotpnl.Execution, lastUpdate int64) error {
	tradesJSON, err := json.Marshal(trades)
	if err != nil {
		return err
	}

	query := `INSERT OR REPLACE INTO trade_history (user_id, trades, last_update) VALUES (?, ?, ?);`
	_, err = DB.Exec(query, userID, string(tradesJSON), lastUpdate)
	return err
}

func GetTradesFromCache(userID int64) ([]spotpnl.Execution, int64, error) {
	query := "SELECT trades, last_update FROM trade_history WHERE user_id = ?"

	row := DB.QueryRow(query, userID)

	var tradesJSON string
	var lastUpdate int64

	err := row.Scan(&tradesJSON, &lastUpdate)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, 0, nil
		}
		return nil, 0, err
	}

	var trades []spotpnl.Execution
	err = json.Unmarshal([]byte(tradesJSON), &trades)
	if err != nil {
		return nil, 0, err
	}

	return trades, lastUpdate, nil
}

func GetTradesHistorySince(client *exchanges.BybitClient, startTime int64) ([]spotpnl.Execution, error) {
	baseURL := "https://api.bybit.com/v5/execution/list"
	httpClient := &http.Client{Timeout: 10 * time.Second}
	var allTrades []spotpnl.Execution

	now := time.Now().UnixMilli()
	currentStart := startTime
	sevenDaysMs := int64(7 * 24 * 60 * 60 * 1000)

	for currentStart < now {
		currentEnd := currentStart + sevenDaysMs
		if currentEnd > now {
			currentEnd = now
		}

		cursor := ""
		for {
			params := url.Values{}
			params.Add("category", "spot")
			params.Add("limit", "100")
			params.Add("startTime", fmt.Sprintf("%d", currentStart))
			params.Add("endTime", fmt.Sprintf("%d", currentEnd))
			if cursor != "" {
				params.Add("cursor", cursor)
			}

			timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())
			recvWindow := "20000"
			queryString := params.Encode()
			signature := client.GenerateSignature(timestamp, recvWindow, queryString)

			fullURL := fmt.Sprintf("%s?%s", baseURL, queryString)
			req, err := http.NewRequest("GET", fullURL, nil)
			if err != nil {
				return nil, fmt.Errorf("ошибка создания запроса истории: %v", err)
			}

			req.Header.Set("X-BAPI-API-KEY", client.ApiKey)
			req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
			req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
			req.Header.Set("X-BAPI-SIGN", signature)

			// Выполняем запрос с retry и строгой проверкой ответа
			var responseData spotpnl.ExecutionResponse
			var body []byte
			var res *http.Response
			var reqErr error
			maxReqAttempts := 3
			for attemptReq := 1; attemptReq <= maxReqAttempts; attemptReq++ {
				res, reqErr = httpClient.Do(req)
				if reqErr != nil {
					if attemptReq < maxReqAttempts {
						log.Printf("[Storage] Попытка %d/%d: ошибка выполнения запроса истории: %v. Повтор через 2 сек...", attemptReq, maxReqAttempts, reqErr)
						time.Sleep(2 * time.Second)
						continue
					}
					return nil, fmt.Errorf("ошибка выполнения запроса истории: %v", reqErr)
				}

				// читаем тело
				body, reqErr = io.ReadAll(res.Body)
				res.Body.Close()
				if reqErr != nil {
					return nil, fmt.Errorf("ошибка чтения ответа истории: %v", reqErr)
				}

				// Проверяем HTTP статус
				if res.StatusCode != 200 {
					log.Printf("[Storage] Неверный HTTP статус %d при запросе истории. Body: %s", res.StatusCode, string(body))
					if attemptReq < maxReqAttempts {
						time.Sleep(2 * time.Second)
						continue
					}
					return nil, fmt.Errorf("API вернул статус %d", res.StatusCode)
				}

				// Пробуем распарсить JSON
				if err := json.Unmarshal(body, &responseData); err != nil {
					// Логируем тело для диагностики, но не возвращаем его пользователю
					log.Printf("[Storage] Ошибка парсинга JSON истории: %v. Body: %s", err, string(body))
					if attemptReq < maxReqAttempts {
						time.Sleep(2 * time.Second)
						continue
					}
					return nil, fmt.Errorf("неверный формат ответа API при получении истории")
				}

				// Успешный парсинг
				break
			}

			if responseData.RetCode != 0 {
				return nil, fmt.Errorf("API ошибка: %s (код %d)", responseData.RetMsg, responseData.RetCode)
			}

			if len(responseData.Result.List) > 0 {
				allTrades = append(allTrades, responseData.Result.List...)
			}

			if responseData.Result.NextPageCursor == "" {
				break
			}

			cursor = responseData.Result.NextPageCursor
			time.Sleep(100 * time.Millisecond)
		}

		currentStart = currentEnd
		time.Sleep(100 * time.Millisecond)
	}

	if len(allTrades) > 0 {
		log.Printf("[Storage] Загружено %d новых сделок", len(allTrades))
	}
	return allTrades, nil
}

func GetAllTradesWithCache(client *exchanges.BybitClient, userID int64) ([]spotpnl.Execution, error) {
	cachedTrades, lastUpdate, err := GetTradesFromCache(userID)
	if err != nil {
		log.Printf("[Cache] Ошибка чтения: %v", err)
	}

	var allTrades []spotpnl.Execution

	if lastUpdate == 0 {
		log.Printf("[Cache] Первая загрузка за 725 дней...")
		now := time.Now()
		startTime := now.AddDate(0, 0, -725).UnixMilli()

		allTrades, err = GetTradesHistorySince(client, startTime)
		if err != nil {
			return nil, err
		}
	} else {
		allTrades = cachedTrades

		newTrades, err := GetTradesHistorySince(client, lastUpdate)
		if err != nil {
			log.Printf("[Cache] Ошибка обновления: %v", err)
			return cachedTrades, nil
		}

		if len(newTrades) > 0 {
			allTrades = append(allTrades, newTrades...)
		}
	}

	currentTime := time.Now().UnixMilli()
	err = SaveTradesToCache(userID, allTrades, currentTime)
	if err != nil {
		log.Printf("[Cache] Ошибка сохранения: %v", err)
	}

	return allTrades, nil
}

func AddAlert(userID int64, symbol string, targetPrice float64, direction string) error {
	query := "INSERT INTO alerts (user_id, symbol, target_price, direction) VALUES (?, ?, ?, ?)"

	_, err := DB.Exec(query, userID, symbol, targetPrice, direction)
	return err
}

func GetAllActiveAlerts() ([]AlertInfo, error) {
	query := "SELECT id, user_id, symbol, target_price, direction FROM alerts WHERE is_active = 1"
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var alerts []AlertInfo
	for rows.Next() {
		var alert AlertInfo
		if err := rows.Scan(&alert.ID, &alert.UserID, &alert.Symbol, &alert.TargetPrice, &alert.Direction); err != nil {
			return nil, err
		}
		alerts = append(alerts, alert)
	}
	return alerts, nil
}

func DeactivateAlert(alertID int) error {
	query := "UPDATE alerts SET is_active = 0 WHERE id = ?"

	_, err := DB.Exec(query, alertID)
	return err
}

func SaveOrUpdateUser(userID int64, apiKey, apiSecret string) error {
	query := `INSERT INTO users (user_id, bybit_api_key, bybit_api_secret, notifications_enabled)
	          VALUES (?, ?, ?, 0)
	          ON CONFLICT(user_id) DO UPDATE SET
	          bybit_api_key = excluded.bybit_api_key,
	          bybit_api_secret = excluded.bybit_api_secret`
	_, err := DB.Exec(query, userID, apiKey, apiSecret)
	return err
}

func SetNotificationsEnabled(userID int64, enabled bool) error {
	var enabledInt int
	if enabled {
		enabledInt = 1
	}

	// Сначала проверяем, существует ли пользователь
	var exists int
	checkQuery := "SELECT COUNT(*) FROM users WHERE user_id = ?"
	DB.QueryRow(checkQuery, userID).Scan(&exists)

	// Если пользователя нет, создаем его
	if exists == 0 {
		insertQuery := "INSERT INTO users (user_id, notifications_enabled) VALUES (?, ?)"
		_, err := DB.Exec(insertQuery, userID, enabledInt)
		return err
	}

	// Если есть, обновляем
	query := "UPDATE users SET notifications_enabled = ? WHERE user_id = ?"
	_, err := DB.Exec(query, enabledInt, userID)
	return err
}

func SavePortfolioSnapshot(userID int64, value float64) error {
	query := "INSERT INTO portfolio_snapshots (user_id, portfolio_value, timestamp) VALUES (?, ?, ?)"
	_, err := DB.Exec(query, userID, value, time.Now().Unix())
	return err
}

func GetLatestSnapshotBefore(userID int64, beforeTimestamp int64) (float64, error) {
	query := "SELECT portfolio_value FROM portfolio_snapshots WHERE user_id = ? AND timestamp < ? ORDER BY timestamp DESC LIMIT 1"

	row := DB.QueryRow(query, userID, beforeTimestamp)

	var value float64
	err := row.Scan(&value)
	return value, err
}

func GetUserSettings(userID int64) (UserSettings, error) {
	query := "SELECT notifications_enabled FROM users WHERE user_id = ?"
	row := DB.QueryRow(query, userID)

	var notificationsEnabled int
	err := row.Scan(&notificationsEnabled)
	if err != nil {
		if err == sql.ErrNoRows {
			// Если пользователя нет, создаем запись с выключенными уведомлениями
			insertQuery := "INSERT INTO users (user_id, notifications_enabled) VALUES (?, 0)"
			DB.Exec(insertQuery, userID)
			return UserSettings{UserID: userID, NotificationsEnabled: false}, nil
		}
		return UserSettings{UserID: userID, NotificationsEnabled: false}, err
	}

	settings := UserSettings{
		UserID:               userID,
		NotificationsEnabled: notificationsEnabled == 1,
	}
	return settings, nil
}

func GetUsersWithNotificationsEnabled() ([]User, error) {
	query := `SELECT user_id, bybit_api_key, bybit_api_secret 
	          FROM users 
	          WHERE notifications_enabled = 1 
	          AND bybit_api_key IS NOT NULL 
	          AND bybit_api_key != ''
	          AND bybit_api_secret IS NOT NULL
	          AND bybit_api_secret != ''`
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.UserID, &u.ApiKey, &u.ApiSecret); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	log.Printf("[GetUsersWithNotificationsEnabled] Найдено пользователей: %d", len(users))
	return users, nil
}
