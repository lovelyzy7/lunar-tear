package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"lunar-tear/server/internal/database"
	"lunar-tear/server/internal/gacha"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store/sqlite"
)

// masterDataPath 可用环境变量 LUNAR_MASTERDATA 覆盖（多区域/自定义路径）。
func masterDataPath() string {
	if v := os.Getenv("LUNAR_MASTERDATA"); v != "" {
		return v
	}
	return "assets/release/20240404193219.bin.e"
}

// envOr returns the environment value of key, or fallback when it is unset.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	listen := flag.String("listen", "0.0.0.0:443", "gRPC listen address (host:port)")
	publicAddr := flag.String("public-addr", "127.0.0.1:443", "externally-reachable host:port advertised to clients")
	dbPath := flag.String("db", "db/game.db", "SQLite database path")
	octoURL := flag.String("octo-url", "", "Octo CDN base URL the client will use for assets (e.g. http://10.0.2.2:8080)")
	authURL := flag.String("auth-url", "", "Auth server base URL for Facebook token validation (e.g. http://localhost:3000)")
	adminListen := flag.String("admin-listen", "127.0.0.1:8082", "admin webhook listen address (host:port). Loopback by default; only binds when LUNAR_ADMIN_TOKEN is set.")
	noRegister := flag.Bool("no-register", false, "Disallow new account registrations for clients, when present. Default = false")
	calendarPath := flag.String("calendar", "", "loop calendar.json path (default: LUNAR_CALENDAR or calendar.json next to the database). Missing file = calendar off (real clock, gacha windows borrowed).")
	flag.Parse()

	if *octoURL == "" {
		log.Fatalf("--octo-url is required (e.g. http://10.0.2.2:8080)")
	}

	// The Original loop calendar (see internal/gametime/calendar.go): the game
	// clock runs at real time + shift, so every rotating term in the 2024-04
	// game data — mission pass, event quests, login stamps, Subjugation Battle,
	// Labyrinth seasons, summons — comes alive on its own schedule again. It
	// must be set before the master data loads: the gacha catalog skips its
	// window borrow (the deployed fallback) while a calendar is running.
	calPath := *calendarPath
	if calPath == "" {
		calPath = envOr("LUNAR_CALENDAR", filepath.Join(filepath.Dir(*dbPath), "calendar.json"))
	}
	calendar, err := gametime.LoadCalendar(calPath)
	if err != nil {
		log.Fatalf("init calendar: %v", err)
	}
	if calendar != nil {
		master := masterDataPath()
		if calendar, err = gametime.StartNextLoop(calPath, master, calendar, time.Now().UnixMilli()); err != nil {
			log.Fatalf("calendar loop swap: %v", err)
		}
		if calendar, err = gametime.RewindLoop(calPath, calendar, time.Now().UnixMilli()); err != nil {
			log.Fatalf("calendar rewind: %v", err)
		}
		if calendar, err = gametime.OpenAtFirstSummons(calPath, calendar, time.Now().UnixMilli()); err != nil {
			log.Fatalf("calendar first summons: %v", err)
		}
	}
	gametime.SetCalendar(calendar)
	if calendar == nil {
		log.Printf("calendar: off (no %s) — real clock, gacha schedule windows borrowed", calPath)
	} else {
		log.Printf("calendar: on (%s) loop %d, loopStart=%d, jump=%dms, speed=%v; game clock now %d (real+%dms)",
			calPath, calendar.Loop, calendar.LoopStart, calendar.JumpMillis, calendar.Speed,
			gametime.NowMillis(), calendar.Shift(time.Now().UnixMilli()))
	}

	holder, err := runtime.NewHolder(masterDataPath())
	if err != nil {
		log.Fatalf("init master data: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.Open(*dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	log.Printf("database opened: %s", *dbPath)

	// The summon and roulette rates live next to the save: gacha_rates.json and
	// roulette_rates.json in the database's folder, so the web panel can edit
	// them and a running server picks a change up (both are polled by mtime).
	ratesDir := filepath.Dir(*dbPath)
	gachaRates := envOr("LUNAR_GACHA_RATES", filepath.Join(ratesDir, "gacha_rates.json"))
	rouletteRates := envOr("LUNAR_ROULETTE_RATES", filepath.Join(ratesDir, "roulette_rates.json"))
	gacha.SetRatesPath(gachaRates)
	gacha.SetRouletteRatesPath(rouletteRates)
	log.Printf("summon rates: %s | roulette rates: %s", gachaRates, rouletteRates)

	userStore := sqlite.New(db, gametime.Now)

	grpcServer := startGRPC(*listen, *publicAddr, *octoURL, *authURL, userStore, holder, *noRegister)

	startAdmin(*adminListen, holder)

	<-ctx.Done()
	log.Println("shutting down...")

	grpcServer.GracefulStop()
	database.Checkpoint(db)

	log.Println("shutdown complete")
}
