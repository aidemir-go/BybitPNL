#!/bin/bash
cd /Users/a123/go/telegram
if [ -f bot.pid ]; then
    kill $(cat bot.pid)
    rm bot.pid
    echo "Бот остановлен"
else
    pkill -f "go run main.go"
    pkill -f "/exe/main"
    echo "Бот остановлен (PID файл не найден)"
fi

#ps aux | grep -E "go run|/exe/main|telegram" | grep -v grep

#kill 18899 18881

#ps aux | grep -E "go run|/exe/main" | grep -v grep