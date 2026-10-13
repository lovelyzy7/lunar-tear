@echo off
cd /d E:\NieRReincarnation\lunar-tear\server
taskkill /f /im lunar-tear.exe >nul 2>&1
taskkill /f /im octo-cdn.exe >nul 2>&1
taskkill /f /im auth-server.exe >nul 2>&1
timeout /t 1 /nobreak >nul
start "lt" /b bin\lunar-tear.exe --listen 0.0.0.0:8003 --public-addr 192.168.2.6:8003 --db db/game.db --octo-url http://192.168.2.6:8080 --auth-url http://0.0.0.0:3000 > logs\lt.log 2>&1
start "octo" /b bin\octo-cdn.exe --listen 0.0.0.0:8080 --public-addr 192.168.2.6:8080 > logs\octo.log 2>&1
start "auth" /b bin\auth-server.exe --listen 0.0.0.0:3000 --db db/auth.db --game-db db/game.db > logs\auth.log 2>&1
