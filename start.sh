#!/bin/bash
cd /Users/a123/go/telegram
go build -o telegram-bot main.go
nohup ./telegram-bot > bot.log 2>&1 &
echo $! > bot.pid
echo "Бот запущен, PID: $(cat bot.pid)"
