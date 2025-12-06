package spotpnl

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"telegram-date-bot/exchanges"
)

type Execution struct {
	Symbol   string `json:"symbol"`
	Price    string `json:"execPrice"`
	Quantity string `json:"execQty"`
	Side     string `json:"side"`
}

type ExecutionResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List           []Execution `json:"list"`
		NextPageCursor string      `json:"nextPageCursor"`
	} `json:"result"`
}

type TickerInfo struct {
	Symbol    string `json:"symbol"`
	LastPrice string `json:"lastPrice"`
}

type TickerResponse struct {
	Result struct {
		List []TickerInfo `json:"list"`
	} `json:"result"`
}

type PortfolioAsset struct {
	Coin          string
	Quantity      float64
	AvgBuyPrice   float64
	CurrentPrice  float64
	UnrealizedPNL float64
	PNLPercentage float64
}

type DisplayAsset struct {
	Name          string
	Symbol        string
	Quantity      float64
	CurrentPrice  float64
	CurrentValue  float64
	AvgBuyPrice   float64
	UnrealizedPNL float64
	PNLPercentage float64
}

type TradeAnalysis struct {
	Symbol      string
	AvgBuyPrice float64
}

func GetTradeHistory(client *exchanges.BybitClient, symbol string) ([]Execution, error) {
	baseURL := "https://api.bybit.com/v5/execution/list"
	httpClient := &http.Client{Timeout: 10 * time.Second}
	var allTrades []Execution

	now := time.Now()
	maxDaysBack := 730
	chunkDays := 7

	for daysBack := 0; daysBack < maxDaysBack; daysBack += chunkDays {
		endTime := now.AddDate(0, 0, -daysBack).UnixMilli()
		startTime := now.AddDate(0, 0, -(daysBack + chunkDays)).UnixMilli()

		cursor := ""

		for {
			params := url.Values{}
			params.Add("category", "spot")
			params.Add("symbol", symbol)
			params.Add("limit", "100")
			params.Add("startTime", fmt.Sprintf("%d", startTime))
			params.Add("endTime", fmt.Sprintf("%d", endTime))
			if cursor != "" {
				params.Add("cursor", cursor)
			}

			timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())
			recvWindow := "5000"
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

			res, err := httpClient.Do(req)
			if err != nil {
				return nil, fmt.Errorf("ошибка выполнения запроса истории: %v", err)
			}
			defer res.Body.Close()

			body, err := io.ReadAll(res.Body)
			if err != nil {
				return nil, fmt.Errorf("ошибка чтения ответа истории: %v", err)
			}

			var responseData ExecutionResponse
			if err := json.Unmarshal(body, &responseData); err != nil {
				return nil, fmt.Errorf("ошибка парсинга JSON истории: %s", string(body))
			}

			if responseData.RetCode != 0 {
				return nil, fmt.Errorf("API ошибка для %s: %s (код %d)", symbol, responseData.RetMsg, responseData.RetCode)
			}

			allTrades = append(allTrades, responseData.Result.List...)

			if responseData.Result.NextPageCursor == "" {
				break
			}
			cursor = responseData.Result.NextPageCursor
			time.Sleep(100 * time.Millisecond)
		}

		if len(allTrades) > 0 {
			log.Printf("[GetTradeHistory] %s: найдено %d сделок за период %d-%d дней назад",
				symbol, len(allTrades), daysBack, daysBack+chunkDays)
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	log.Printf("[GetTradeHistory] %s: всего сделок %d", symbol, len(allTrades))
	return allTrades, nil
}

func CalculateAverageBuyPrice(trades []Execution) float64 {
	var totalCost, totalQuantity float64

	for _, trade := range trades {
		if trade.Side == "Buy" {
			price, _ := strconv.ParseFloat(trade.Price, 64)
			quantity, _ := strconv.ParseFloat(trade.Quantity, 64)
			totalCost += price * quantity
			totalQuantity += quantity
		}
	}

	if totalQuantity == 0 {
		return 0
	}
	return totalCost / totalQuantity
}

func GetCurrentPrice(symbol string) (float64, error) {
	url := fmt.Sprintf("https://api.bybit.com/v5/market/tickers?category=spot&symbol=%s", symbol)
	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var tickerData TickerResponse
	if err := json.Unmarshal(body, &tickerData); err != nil {
		return 0, err
	}

	if len(tickerData.Result.List) == 0 {
		return 0, fmt.Errorf("цена для %s не найдена", symbol)
	}

	return strconv.ParseFloat(tickerData.Result.List[0].LastPrice, 64)
}

func CalculatePortfolioPNL(client *exchanges.BybitClient) ([]PortfolioAsset, error) {
	balances, err := client.GetSpotBalance()
	if err != nil {
		return nil, fmt.Errorf("ошибка получения баланса: %v", err)
	}

	log.Printf("[PNL] Получен баланс: %+v", balances)
	var portfolio []PortfolioAsset

	for coin, quantityStr := range balances {
		if coin == "USDT" || coin == "USDC" || coin == "DAI" || coin == "TOTAL" {
			log.Printf("[PNL] Пропускаем %s (стейблкоин или TOTAL)", coin)
			continue
		}

		quantity, err := strconv.ParseFloat(quantityStr, 64)
		if err != nil {
			log.Printf("[PNL] Ошибка конвертации количества для %s: %v", coin, err)
			continue
		}

		if quantity == 0 {
			log.Printf("[PNL] Пропускаем %s (нулевой баланс)", coin)
			continue
		}

		symbol := coin + "USDT"
		log.Printf("[PNL] Обрабатываем %s, количество: %f", symbol, quantity)

		tradeHistory, err := GetTradeHistory(client, symbol)
		if err != nil {
			log.Printf("[PNL] Не удалось получить историю для %s: %v", symbol, err)
			continue
		}
		log.Printf("[PNL] Получено сделок для %s: %d", symbol, len(tradeHistory))

		avgBuyPrice := CalculateAverageBuyPrice(tradeHistory)
		log.Printf("[PNL] Средняя цена покупки %s: %.2f", symbol, avgBuyPrice)

		if avgBuyPrice == 0 {
			log.Printf("[PNL] Пропускаем %s (нет истории покупок)", symbol)
			continue
		}

		currentPrice, err := GetCurrentPrice(symbol)
		if err != nil {
			log.Printf("[PNL] Не удалось получить цену для %s: %v", symbol, err)
			currentPrice = 0
		}
		log.Printf("[PNL] Текущая цена %s: %.2f", symbol, currentPrice)

		log.Printf("[PNL] Текущая цена %s: %.2f", symbol, currentPrice)

		unrealizedPNL := (currentPrice - avgBuyPrice) * quantity
		pnlPercentage := (unrealizedPNL / (avgBuyPrice * quantity)) * 100

		asset := PortfolioAsset{
			Coin:          coin,
			Quantity:      quantity,
			AvgBuyPrice:   avgBuyPrice,
			CurrentPrice:  currentPrice,
			UnrealizedPNL: unrealizedPNL,
			PNLPercentage: pnlPercentage,
		}
		log.Printf("[PNL] Добавлен актив: %s, PNL: %.2f$ (%.2f%%)", coin, unrealizedPNL, pnlPercentage)
		portfolio = append(portfolio, asset)
	}

	log.Printf("[PNL] Итого активов в портфеле: %d", len(portfolio))
	return portfolio, nil
}

func FormatPortfolioMessage(portfolio []PortfolioAsset) string {
	if len(portfolio) == 0 {
		return "Не удалось рассчитать PnL. Возможно, на спотовом аккаунте нет монет (кроме USDT) или отсутствует история сделок."
	}

	var messageBuilder strings.Builder
	var totalPNL float64

	messageBuilder.WriteString("📈 *Ваш спотовый портфель:*\n\n" + "```")

	for _, asset := range portfolio {
		totalPNL += asset.UnrealizedPNL
		emoji := "🟢"
		if asset.UnrealizedPNL < 0 {
			emoji = "🔴"
		}

		assetString := fmt.Sprintf(
			"\n%s %s | PNL: %.2f$ (%.2f%%)\nКол-во: %f\nAvg. цена: %.2f$\n",
			emoji, asset.Coin, asset.UnrealizedPNL, asset.PNLPercentage, asset.Quantity, asset.AvgBuyPrice,
		)
		messageBuilder.WriteString(assetString)
	}

	messageBuilder.WriteString(fmt.Sprintf("```"+"\n*Общий PNL: %.2f$*", totalPNL))
	return messageBuilder.String()
}

func GroupTradesBySymbol(trades []Execution) map[string][]Execution {
	grouped := make(map[string][]Execution)
	for _, trade := range trades {
		grouped[trade.Symbol] = append(grouped[trade.Symbol], trade)
	}
	return grouped
}

func AnalyzeTradeHistory(groupedTrades map[string][]Execution) map[string]TradeAnalysis {
	result := make(map[string]TradeAnalysis)

	for symbol, trades := range groupedTrades {
		var totalCost, totalQuantity float64

		for _, trade := range trades {
			if trade.Side == "Buy" {
				price, _ := strconv.ParseFloat(trade.Price, 64)
				quantity, _ := strconv.ParseFloat(trade.Quantity, 64)
				totalCost += price * quantity
				totalQuantity += quantity
			}
		}

		var avgBuyPrice float64
		if totalQuantity > 0 {
			avgBuyPrice = totalCost / totalQuantity
		}

		result[symbol] = TradeAnalysis{
			Symbol:      symbol,
			AvgBuyPrice: avgBuyPrice,
		}
	}

	return result
}

func FormatBalancePNLMessage(assets []DisplayAsset) string {
	if len(assets) == 0 {
		return "💼 Портфель пуст"
	}

	var messageBuilder strings.Builder
	var totalPortfolioValue, totalUnrealizedPNL float64

	messageBuilder.WriteString("📈 *Ваш спотовый портфель:*\n\n```\n")
	messageBuilder.WriteString(fmt.Sprintf("%-8s | %-10s | %s\n", "Актив", "Кол-во", "PNL $ (%)"))
	messageBuilder.WriteString("---------------------------------------\n")

	for _, asset := range assets {
		if asset.Quantity == 0 {
			continue
		}

		totalPortfolioValue += asset.CurrentValue
		totalUnrealizedPNL += asset.UnrealizedPNL

		pnlEmoji := "⚪"
		if asset.UnrealizedPNL > 0 {
			pnlEmoji = "🟢"
		}
		if asset.UnrealizedPNL < 0 {
			pnlEmoji = "🔴"
		}

		line := fmt.Sprintf("%-8s | %-10.4f | %s%.2f (%.1f%%)\n",
			asset.Name,
			asset.Quantity,
			pnlEmoji,
			asset.UnrealizedPNL,
			asset.PNLPercentage,
		)
		messageBuilder.WriteString(line)
	}

	messageBuilder.WriteString("```\n")
	messageBuilder.WriteString(fmt.Sprintf("*Общая стоимость: %.2f$*\n", totalPortfolioValue))
	messageBuilder.WriteString(fmt.Sprintf("*Общий PNL: %.2f$*", totalUnrealizedPNL))

	return messageBuilder.String()
}
