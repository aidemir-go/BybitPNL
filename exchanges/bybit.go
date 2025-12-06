package exchanges

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

type BybitClient struct {
	ApiKey    string
	ApiSecret string
}

type BalanceResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List []struct {
			TotalWalletBalance string `json:"totalWalletBalance"`
			Coin               []struct {
				Coin   string `json:"coin"`
				Equity string `json:"equity"`
			} `json:"coin"`
		} `json:"list"`
	} `json:"result"`
}

func NewBybitClient(apiKey, apiSecret string) *BybitClient {
	return &BybitClient{
		ApiKey:    apiKey,
		ApiSecret: apiSecret,
	}
}

func (c *BybitClient) GetSpotBalance() (map[string]string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", "https://api.bybit.com/v5/account/wallet-balance?accountType=UNIFIED", nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка создания запроса: %v", err)
	}

	timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())
	recvWindow := "20000"
	params := "accountType=UNIFIED"

	req.Header.Add("X-BAPI-API-KEY", c.ApiKey)
	req.Header.Add("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Add("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Add("X-BAPI-SIGN", c.GenerateSignature(timestamp, recvWindow, params))

	// Выполняем запрос с retry и проверкой статуса/тела
	var resp *http.Response
	var reqErr error
	var body []byte
	maxAttempts := 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, reqErr = client.Do(req)
		if reqErr != nil {
			if attempt < maxAttempts {
				log.Printf("[Bybit] Попытка %d/%d: ошибка выполнения запроса баланса: %v. Повтор через 2 сек...", attempt, maxAttempts, reqErr)
				time.Sleep(2 * time.Second)
				continue
			}
			return nil, fmt.Errorf("ошибка выполнения запроса: %v", reqErr)
		}

		body, reqErr = io.ReadAll(resp.Body)
		resp.Body.Close()
		if reqErr != nil {
			return nil, fmt.Errorf("ошибка чтения ответа: %v", reqErr)
		}

		if resp.StatusCode != 200 {
			// 401 - вероятно проблема с ключами/whitelist
			log.Printf("[Bybit] Неверный HTTP статус %d при получении баланса. Body: %s", resp.StatusCode, string(body))
			if resp.StatusCode == 401 {
				return nil, fmt.Errorf("неавторизовано: проверьте API-ключ/секрет и IP-whitelist")
			}
			if attempt < maxAttempts {
				time.Sleep(2 * time.Second)
				continue
			}
			return nil, fmt.Errorf("API вернул статус %d", resp.StatusCode)
		}

		break
	}

	var balanceResp BalanceResponse
	if err := json.Unmarshal(body, &balanceResp); err != nil {
		log.Printf("[Bybit] Ошибка парсинга JSON баланса: %v. Body: %s", err, string(body))
		return nil, fmt.Errorf("неверный формат ответа API при получении баланса")
	}

	if balanceResp.RetCode != 0 {
		return nil, fmt.Errorf("API ошибка: %s", balanceResp.RetMsg)
	}

	const minBalance = 0.01
	balances := make(map[string]string)
	totalBalance := balanceResp.Result.List[0].TotalWalletBalance
	balances["TOTAL"] = totalBalance

	if len(balanceResp.Result.List) > 0 {
		for _, coin := range balanceResp.Result.List[0].Coin {
			equity := coin.Equity
			if equity != "0" && equity != "" {
				if value, err := strconv.ParseFloat(equity, 64); err == nil && value >= minBalance {
					balances[coin.Coin] = equity
				}
			}
		}
	}
	return balances, nil
}

func (c *BybitClient) GenerateSignature(timestamp string, recvWindow string, params string) string {
	data := timestamp + c.ApiKey + recvWindow + params
	h := hmac.New(sha256.New, []byte(c.ApiSecret))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
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

func (c *BybitClient) GetMarketTickers(category string) (map[string]TickerInfo, error) {
	url := fmt.Sprintf("https://api.bybit.com/v5/market/tickers?category=%s", category)

	client := &http.Client{Timeout: 10 * time.Second}
	var resp *http.Response
	var reqErr error
	var body []byte
	maxAttempts := 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, reqErr = client.Get(url)
		if reqErr != nil {
			if attempt < maxAttempts {
				log.Printf("[Bybit] Попытка %d/%d: ошибка получения тикеров: %v. Повтор через 2 сек...", attempt, maxAttempts, reqErr)
				time.Sleep(2 * time.Second)
				continue
			}
			return nil, reqErr
		}
		body, reqErr = io.ReadAll(resp.Body)
		resp.Body.Close()
		if reqErr != nil {
			return nil, reqErr
		}
		if resp.StatusCode != 200 {
			log.Printf("[Bybit] Неверный HTTP статус %d при получении тикеров. Body: %s", resp.StatusCode, string(body))
			if attempt < maxAttempts {
				time.Sleep(2 * time.Second)
				continue
			}
			return nil, fmt.Errorf("API вернул статус %d", resp.StatusCode)
		}
		break
	}

	var responseData TickerResponse
	if err := json.Unmarshal(body, &responseData); err != nil {
		log.Printf("[Bybit] Ошибка парсинга JSON тикеров: %v. Body: %s", err, string(body))
		return nil, fmt.Errorf("неверный формат ответа API при получении тикеров")
	}

	tickersMap := make(map[string]TickerInfo)
	for _, ticker := range responseData.Result.List {
		tickersMap[ticker.Symbol] = ticker
	}

	return tickersMap, nil
}

func (c *BybitClient) GetAllMarketPrices() (map[string]float64, error) {
	url := "https://api.bybit.com/v5/market/tickers?category=spot"

	client := &http.Client{Timeout: 10 * time.Second}
	var resp *http.Response
	var reqErr error
	var body []byte
	maxAttempts := 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, reqErr = client.Get(url)
		if reqErr != nil {
			if attempt < maxAttempts {
				log.Printf("[Bybit] Попытка %d/%d: ошибка получения цен: %v. Повтор через 2 сек...", attempt, maxAttempts, reqErr)
				time.Sleep(2 * time.Second)
				continue
			}
			return nil, reqErr
		}
		body, reqErr = io.ReadAll(resp.Body)
		resp.Body.Close()
		if reqErr != nil {
			return nil, reqErr
		}
		if resp.StatusCode != 200 {
			log.Printf("[Bybit] Неверный HTTP статус %d при получении цен. Body: %s", resp.StatusCode, string(body))
			if attempt < maxAttempts {
				time.Sleep(2 * time.Second)
				continue
			}
			return nil, fmt.Errorf("API вернул статус %d", resp.StatusCode)
		}
		break
	}

	var responseData TickerResponse
	if err := json.Unmarshal(body, &responseData); err != nil {
		log.Printf("[Bybit] Ошибка парсинга JSON цен: %v. Body: %s", err, string(body))
		return nil, fmt.Errorf("неверный формат ответа API при получении цен")
	}

	pricesMap := make(map[string]float64)
	for _, ticker := range responseData.Result.List {
		price, err := strconv.ParseFloat(ticker.LastPrice, 64)
		if err == nil {
			pricesMap[ticker.Symbol] = price
		}
	}

	return pricesMap, nil
}
