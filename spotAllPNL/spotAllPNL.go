package spotAllPNL

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"telegram-date-bot/exchanges"
	"time"
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

type TradeAnalysis struct {
	Symbol              string
	TotalCost           float64 // Сколько всего потрачено USDT на покупки
	TotalRevenue        float64 // Сколько всего получено USDT от продаж
	TotalQuantityBought float64 // Сколько всего монет куплено
	TotalQuantitySold   float64 // Сколько всего монет продано
	AvgBuyPrice         float64
	RealizedPNL         float64
}

type DisplayAsset struct {
	Name           string  // "BTC"
	Symbol         string  // "BTCUSDT"
	Quantity       float64 // Количество на балансе
	CurrentPrice   float64 // Текущая цена
	CurrentValue   float64 // Текущая стоимость (Quantity * CurrentPrice)
	AvgBuyPrice    float64 // Средняя цена покупки
	UnrealizedPNL  float64 // Нереализованный PnL в $
	PNLPercentage  float64 // PnL в %
}

func GetAllTradesHistory(client *exchanges.BybitClient) ([]Execution, error) {
	baseURL := "https://api.bybit.com/v5/execution/list"
	httpClient := &http.Client{Timeout: 10 * time.Second}
	var allTrades []Execution

	now := time.Now()
	maxDaysBack := 728
	chunkDays := 7

	log.Printf("[GetAllTradesHistory] Начинаем сбор истории за %d дней", maxDaysBack)

	for daysBack := 0; daysBack < maxDaysBack; daysBack += chunkDays {
		endTime := now.AddDate(0, 0, -daysBack).UnixMilli()
		startTime := now.AddDate(0, 0, -(daysBack + chunkDays)).UnixMilli()

		log.Printf("[GetAllTradesHistory] Период: %d-%d дней назад", daysBack, daysBack+chunkDays)

		cursor := ""
		for {
			params := url.Values{}
			params.Add("category", "spot")
			params.Add("limit", "100")
			params.Add("startTime", fmt.Sprintf("%d", startTime))
			params.Add("endTime", fmt.Sprintf("%d", endTime))
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
				return nil, fmt.Errorf("ошибка создания запроса всей истории: %v", err)
			}

			req.Header.Set("X-BAPI-API-KEY", client.ApiKey)
			req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
			req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
			req.Header.Set("X-BAPI-SIGN", signature)

			res, err := httpClient.Do(req)
			if err != nil {
				return nil, fmt.Errorf("ошибка выполнения запроса всей истории: %v", err)
			}
			defer res.Body.Close()

			body, err := io.ReadAll(res.Body)
			if err != nil {
				return nil, fmt.Errorf("ошибка чтения ответа всей истории: %v", err)
			}

			var responseData ExecutionResponse
			if err := json.Unmarshal(body, &responseData); err != nil {
				return nil, fmt.Errorf("ошибка парсинга JSON всей истории: %s", string(body))
			}

			if responseData.RetCode != 0 {
				return nil, fmt.Errorf("API ошибка: %s (код %d)", responseData.RetMsg, responseData.RetCode)
			}

			if len(responseData.Result.List) > 0 {
				allTrades = append(allTrades, responseData.Result.List...)
				log.Printf("[GetAllTradesHistory] +%d сделок", len(responseData.Result.List))
			}

			if responseData.Result.NextPageCursor == "" {
				break
			}

			cursor = responseData.Result.NextPageCursor
			time.Sleep(100 * time.Millisecond)
		}

		time.Sleep(100 * time.Millisecond)
	}

	log.Printf("[GetAllTradesHistory] Всего: %d сделок", len(allTrades))
	return allTrades, nil
}

func GroupTradesBySymbol(allTrades []Execution) map[string][]Execution {
	groupedTrades := make(map[string][]Execution)

	for _, trade := range allTrades {
		groupedTrades[trade.Symbol] = append(groupedTrades[trade.Symbol], trade)
	}
	return groupedTrades
}

func AnalyzeTradeHistory(groupedTrades map[string][]Execution) map[string]TradeAnalysis {
	analysisResult := make(map[string]TradeAnalysis)

	for symbol, trades := range groupedTrades {
		var totalCost, totalRevenue, totalQuantityBought, totalQuantitySold float64

		for _, trade := range trades {
			price, _ := strconv.ParseFloat(trade.Price, 64)
			quantity, _ := strconv.ParseFloat(trade.Quantity, 64)

			switch trade.Side {
			case "Buy":
				totalCost += price * quantity
				totalQuantityBought += quantity
			case "Sell":
				totalRevenue += price * quantity
				totalQuantitySold += quantity
			}
		}

		var avgBuyPrice, realizedPNL float64
		if totalQuantityBought > 0 {
			avgBuyPrice = totalCost / totalQuantityBought
		}

		if totalQuantitySold > 0 {
			costOfGoodsSold := totalQuantitySold * avgBuyPrice
			realizedPNL = totalRevenue - costOfGoodsSold
		}

		analysisResult[symbol] = TradeAnalysis{
			Symbol:              symbol,
			TotalCost:           totalCost,
			TotalRevenue:        totalRevenue,
			TotalQuantityBought: totalQuantityBought,
			TotalQuantitySold:   totalQuantitySold,
			AvgBuyPrice:         avgBuyPrice,
			RealizedPNL:         realizedPNL,
		}
	}
	return analysisResult
}

func FormatTotalPNLMessage(analysis map[string]TradeAnalysis) string {
	if len(analysis) == 0 {
		return "История сделок не найдена."
	}

var relevantAssets []TradeAnalysis

for _, relevantAsset := range analysis {relevantAssets = append(relevantAssets, relevantAsset)}

sort.Slice(relevantAssets, func(i, j int) bool {	
		return math.Abs(relevantAssets[i].RealizedPNL) > math.Abs(relevantAssets[j].RealizedPNL)
	})

if len(relevantAssets) == 0 {return "История сделок не найдена."}

var messageBuilder strings.Builder
	var totalRealizedPNL float64

	messageBuilder.WriteString("📊 *Отчет по реализованному PnL:*\n\n")
	messageBuilder.WriteString("`") 
	messageBuilder.WriteString(fmt.Sprintf("%-12s | %-10s | %-7s\n", "Актив", "PnL ($)", "ROI (%)"))
	messageBuilder.WriteString("-------------------------------------\n")

	for _, asset := range relevantAssets {
		totalRealizedPNL += asset.RealizedPNL

		// Считаем ROI
		costOfSold := asset.TotalQuantitySold * asset.AvgBuyPrice
		roi := 0.0
		if costOfSold > 0 {
			roi = (asset.RealizedPNL / costOfSold) * 100
		}
		
		messageBuilder.WriteString(fmt.Sprintf("%-12s | %-10.2f | %-7.2f\n", asset.Symbol, asset.RealizedPNL, roi))
	}
		messageBuilder.WriteString("`\n") 
	messageBuilder.WriteString(fmt.Sprintf("\n*Общий итог: %.2f$*", totalRealizedPNL))

	return messageBuilder.String()
}

func ExportToCSV(analysis map[string]TradeAnalysis) ([]byte, error) {
	var buffer bytes.Buffer 
	writer := csv.NewWriter(&buffer)

	header := []string{"Символ", "Реализованный PNL", "Всего потрачено", "Всего получено", "Средняя цена покупки", "Куплено", "Продано"}
	if err := writer.Write(header); err != nil {
		return nil, err
	}
	for _, asset := range analysis {
		record := []string{
			asset.Symbol,
			fmt.Sprintf("%.2f", asset.RealizedPNL),
			fmt.Sprintf("%.2f", asset.TotalCost),
			fmt.Sprintf("%.2f", asset.TotalRevenue),
			fmt.Sprintf("%.2f", asset.AvgBuyPrice),
			fmt.Sprintf("%.4f", asset.TotalQuantityBought),
			fmt.Sprintf("%.4f", asset.TotalQuantitySold),
		}

		if err := writer.Write(record); err != nil {
			return nil, err
		}
	}
	writer.Flush()
if err := writer.Error(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

