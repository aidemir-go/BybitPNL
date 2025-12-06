package database

import (
	"encoding/json"
	"os"
)

type User struct {
	ChatID         int64  `json:"chat_id"`
	BybitApiKey    string `json:"bybit_api_key"`
	BybitApiSecret string `json:"bybit_api_secret"`
}

func LoadUsers() (map[int64]User, error) {
	users := make(map[int64]User)

	data, err := os.ReadFile("users.json")
	if err != nil {
		if os.IsNotExist(err) {
			return users, nil
		}
		return nil, err
	}

	err = json.Unmarshal(data, &users)
	return users, err
}

func SaveUser(user User) error {
	users, err := LoadUsers()
	if err != nil {
		return err
	}

	users[user.ChatID] = user

	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile("users.json", data, 0644)
}

func GetUser(chatID int64) (User, error) {
	users, err := LoadUsers()
	if err != nil {
		return User{}, err
	}

	user, exists := users[chatID]
	if !exists {
		return User{}, nil
	}

	return user, nil
}
